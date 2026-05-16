use std::collections::HashMap;
use std::time::Duration;

use reqwest::Client;
use serde_json::json;

use crate::duration::format_duration;
use crate::error::{raise_for_status, Error, Result};
use crate::types::*;

const RETRY_STATUSES: [u16; 3] = [502, 503, 504];
const BACKOFF_SCHEDULE: [Duration; 3] = [
    Duration::from_millis(500),
    Duration::from_millis(1000),
    Duration::from_millis(2000),
];

pub struct WorkqueueClient {
    endpoint: String,
    client: Client,
    retries: u32,
}

pub struct WorkqueueClientBuilder {
    endpoint: String,
    client: Option<Client>,
    timeout: Duration,
    retries: u32,
}

impl WorkqueueClientBuilder {
    pub fn new(endpoint: &str) -> Self {
        Self {
            endpoint: endpoint.trim_end_matches('/').to_string(),
            client: None,
            timeout: Duration::from_secs(30),
            retries: 3,
        }
    }

    pub fn client(mut self, client: Client) -> Self {
        self.client = Some(client);
        self
    }

    pub fn timeout(mut self, timeout: Duration) -> Self {
        self.timeout = timeout;
        self
    }

    pub fn retries(mut self, retries: u32) -> Self {
        self.retries = retries;
        self
    }

    pub fn build(self) -> WorkqueueClient {
        let client = self.client.unwrap_or_else(|| {
            Client::builder()
                .timeout(self.timeout)
                .build()
                .expect("failed to build HTTP client")
        });
        WorkqueueClient {
            endpoint: self.endpoint,
            client,
            retries: self.retries,
        }
    }
}

impl WorkqueueClient {
    pub fn new(endpoint: &str) -> Self {
        WorkqueueClientBuilder::new(endpoint).build()
    }

    pub fn builder(endpoint: &str) -> WorkqueueClientBuilder {
        WorkqueueClientBuilder::new(endpoint)
    }

    pub fn with_client(endpoint: &str, client: Client) -> Self {
        WorkqueueClientBuilder::new(endpoint).client(client).build()
    }

    async fn post(&self, path: &str, payload: serde_json::Value) -> Result<String> {
        let mut last_err: Option<Error> = None;

        for attempt in 0..=self.retries {
            let result = self
                .client
                .post(format!("{}{path}", self.endpoint))
                .json(&payload)
                .send()
                .await;

            match result {
                Ok(resp) => {
                    let status = resp.status().as_u16();
                    let body = resp.text().await?;

                    if RETRY_STATUSES.contains(&status) && attempt < self.retries {
                        let delay = BACKOFF_SCHEDULE[attempt as usize % BACKOFF_SCHEDULE.len()];
                        tokio::time::sleep(delay).await;
                        continue;
                    }

                    raise_for_status(status, body.clone())?;
                    return Ok(body);
                }
                Err(e) if e.is_connect() && attempt < self.retries => {
                    last_err = Some(Error::Http(e));
                    let delay = BACKOFF_SCHEDULE[attempt as usize % BACKOFF_SCHEDULE.len()];
                    tokio::time::sleep(delay).await;
                }
                Err(e) => return Err(Error::Http(e)),
            }
        }

        Err(last_err.unwrap())
    }

    pub async fn enqueue(&self, queue: &str, key: &str, priority: i32) -> Result<()> {
        self.post("/wq/enqueue", json!({"queue": queue, "key": key, "priority": priority}))
            .await?;
        Ok(())
    }

    pub async fn enqueue_batch(&self, queue: &str, items: &[BatchEnqueueItem]) -> Result<i32> {
        let body = self
            .post("/wq/enqueue-batch", json!({"queue": queue, "items": items}))
            .await?;
        let v: serde_json::Value = serde_json::from_str(&body)?;
        Ok(v["count"].as_i64().unwrap_or(0) as i32)
    }

    pub async fn claim_batch(
        &self,
        queue: &str,
        batch_size: i32,
        worker_id: &str,
        lease_duration: Duration,
    ) -> Result<Vec<WorkItem>> {
        let body = self
            .post(
                "/wq/claim",
                json!({
                    "queue": queue,
                    "batch_size": batch_size,
                    "worker_id": worker_id,
                    "lease_duration": format_duration(lease_duration),
                }),
            )
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn complete(&self, queue: &str, key: &str) -> Result<()> {
        self.post("/wq/complete", json!({"queue": queue, "key": key}))
            .await?;
        Ok(())
    }

    pub async fn fail(&self, queue: &str, key: &str, error_msg: &str) -> Result<()> {
        self.post("/wq/fail", json!({"queue": queue, "key": key, "error": error_msg}))
            .await?;
        Ok(())
    }

    pub async fn requeue(&self, queue: &str, key: &str) -> Result<()> {
        self.post("/wq/requeue", json!({"queue": queue, "key": key}))
            .await?;
        Ok(())
    }

    pub async fn deadletter(&self, queue: &str, key: &str) -> Result<()> {
        self.post("/wq/deadletter", json!({"queue": queue, "key": key}))
            .await?;
        Ok(())
    }

    pub async fn extend_lease(&self, queue: &str, key: &str, duration: Duration) -> Result<()> {
        self.post(
            "/wq/heartbeat",
            json!({"queue": queue, "key": key, "duration": format_duration(duration)}),
        )
        .await?;
        Ok(())
    }

    pub async fn transition(
        &self,
        queue: &str,
        key: &str,
        from: &Status,
        to: &Status,
    ) -> Result<()> {
        self.post(
            "/wq/transition",
            json!({"queue": queue, "key": key, "from": from, "to": to}),
        )
        .await?;
        Ok(())
    }

    pub async fn ensure_queue(&self, queue: &str, config: &QueueConfig) -> Result<()> {
        self.post("/wq/ensure-queue", json!({"queue": queue, "config": config}))
            .await?;
        Ok(())
    }

    pub async fn repair_counter(&self, queue: &str) -> Result<()> {
        self.post("/wq/repair", json!({"queue": queue})).await?;
        Ok(())
    }

    pub async fn set_queue_paused(&self, queue: &str, paused: bool) -> Result<()> {
        self.post("/wq/set-paused", json!({"queue": queue, "paused": paused}))
            .await?;
        Ok(())
    }

    pub async fn is_queue_paused(&self, queue: &str) -> Result<bool> {
        let body = self
            .post("/wq/is-paused", json!({"queue": queue}))
            .await?;
        let v: serde_json::Value = serde_json::from_str(&body)?;
        Ok(v["paused"].as_bool().unwrap_or(false))
    }

    pub async fn count_by_status(&self, queue: &str) -> Result<HashMap<Status, i64>> {
        let body = self.post("/wq/count", json!({"queue": queue})).await?;
        let raw: HashMap<String, i64> = serde_json::from_str(&body)?;
        let mut counts = HashMap::new();
        for (k, v) in raw {
            if let Ok(status) = serde_json::from_value::<Status>(serde_json::Value::String(k)) {
                counts.insert(status, v);
            }
        }
        Ok(counts)
    }

    pub async fn list(&self, filter: &ListFilter) -> Result<Vec<WorkItem>> {
        let body = self
            .post("/wq/list", serde_json::to_value(filter)?)
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn get_item(&self, queue: &str, key: &str) -> Result<WorkItem> {
        let body = self
            .post("/wq/get-item", json!({"queue": queue, "key": key}))
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn list_queues(&self) -> Result<Vec<QueueInfo>> {
        let body = self
            .post("/wq/list-queues", serde_json::Value::Null)
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn list_workers(&self, queue: &str) -> Result<Vec<WorkerLease>> {
        let body = self
            .post("/wq/list-workers", json!({"queue": queue}))
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn purge_dead_letters(&self, queue: &str) -> Result<i64> {
        let body = self
            .post("/wq/purge-dead-letters", json!({"queue": queue}))
            .await?;
        let v: serde_json::Value = serde_json::from_str(&body)?;
        Ok(v["count"].as_i64().unwrap_or(0))
    }

    pub async fn list_expired_leases(&self, queue: &str, limit: i32) -> Result<Vec<WorkItem>> {
        let body = self
            .post(
                "/wq/list-expired-leases",
                json!({"queue": queue, "limit": limit}),
            )
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn record_history(&self, entry: &HistoryEntry) -> Result<()> {
        self.post("/wq/record-history", serde_json::to_value(entry)?)
            .await?;
        Ok(())
    }

    pub async fn get_item_history(&self, queue: &str, key: &str) -> Result<Vec<HistoryEntry>> {
        let body = self
            .post("/wq/get-history", json!({"queue": queue, "key": key}))
            .await?;
        Ok(serde_json::from_str(&body)?)
    }

    pub async fn ping(&self) -> Result<()> {
        self.post("/wq/ping", serde_json::Value::Null).await?;
        Ok(())
    }
}
