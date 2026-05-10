use std::time::Duration;

use reqwest::Client;
use serde_json::json;

use crate::error::{Error, Result};

const RETRY_STATUSES: [u16; 3] = [502, 503, 504];
const BACKOFF_SCHEDULE: [Duration; 3] = [
    Duration::from_millis(500),
    Duration::from_millis(1000),
    Duration::from_millis(2000),
];

pub struct EnqueueClient {
    endpoint: String,
    client: Client,
    retries: u32,
}

impl EnqueueClient {
    pub fn new(endpoint: &str) -> Self {
        Self {
            endpoint: endpoint.trim_end_matches('/').to_string(),
            client: Client::builder()
                .timeout(Duration::from_secs(10))
                .build()
                .expect("failed to build HTTP client"),
            retries: 3,
        }
    }

    pub fn with_client(endpoint: &str, client: Client) -> Self {
        Self {
            endpoint: endpoint.trim_end_matches('/').to_string(),
            client,
            retries: 3,
        }
    }

    pub fn with_retries(mut self, retries: u32) -> Self {
        self.retries = retries;
        self
    }

    pub async fn enqueue(&self, queue: &str, key: &str, priority: i32) -> Result<()> {
        let mut last_err: Option<Error> = None;

        for attempt in 0..=self.retries {
            let result = self
                .client
                .post(format!("{}/enqueue", self.endpoint))
                .json(&json!({"queue": queue, "key": key, "priority": priority}))
                .send()
                .await;

            match result {
                Ok(resp) => {
                    let status = resp.status().as_u16();
                    if status == 200 || status == 201 {
                        return Ok(());
                    }
                    if RETRY_STATUSES.contains(&status) && attempt < self.retries {
                        let delay = BACKOFF_SCHEDULE[attempt as usize % BACKOFF_SCHEDULE.len()];
                        tokio::time::sleep(delay).await;
                        continue;
                    }
                    let body = resp.text().await.unwrap_or_default();
                    return Err(Error::Api { status, body });
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
}
