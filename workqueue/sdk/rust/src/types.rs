use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum Status {
    #[serde(rename = "pending")]
    #[default]
    Pending,
    #[serde(rename = "claimed")]
    Claimed,
    #[serde(rename = "running")]
    Running,
    #[serde(rename = "succeeded")]
    Succeeded,
    #[serde(rename = "failed")]
    Failed,
    #[serde(rename = "dead_letter")]
    DeadLetter,
}

impl Status {
    pub fn valid_transition(&self, to: &Status) -> bool {
        use Status::*;
        matches!(
            (self, to),
            (Pending, Claimed)
                | (Pending, Failed)
                | (Claimed, Running)
                | (Claimed, Failed)
                | (Claimed, Pending)
                | (Running, Succeeded)
                | (Running, Failed)
                | (Running, Pending)
                | (Failed, Pending)
                | (Failed, DeadLetter)
                | (DeadLetter, Pending)
        )
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WorkItem {
    pub queue: String,
    pub key: String,
    pub status: Status,
    #[serde(default)]
    pub priority: i32,
    #[serde(default)]
    pub attempts: i32,
    #[serde(default)]
    pub max_attempts: i32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub not_before: Option<DateTime<Utc>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lease_expires: Option<DateTime<Utc>>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub worker_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error_message: String,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub claimed_at: Option<DateTime<Utc>>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<DateTime<Utc>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueueConfig {
    #[serde(default)]
    pub max_concurrency: i32,
    #[serde(default)]
    pub max_retry: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub compute_backend: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct QueueInfo {
    pub name: String,
    #[serde(default)]
    pub max_concurrency: i32,
    #[serde(default)]
    pub max_retry: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub compute_backend: String,
    #[serde(default)]
    pub paused: bool,
    #[serde(default)]
    pub in_progress: i32,
    #[serde(default)]
    pub counts: HashMap<String, i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ListFilter {
    pub queue: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<Status>,
    #[serde(default)]
    pub limit: i32,
    #[serde(default)]
    pub offset: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HistoryEntry {
    #[serde(default)]
    pub id: i64,
    #[serde(default)]
    pub queue: String,
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub from_status: Status,
    #[serde(default)]
    pub to_status: Status,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub worker_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error_message: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub attempt: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub trace_id: String,
    pub created_at: DateTime<Utc>,
}

fn is_zero(v: &i32) -> bool {
    *v == 0
}


#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WorkerLease {
    #[serde(default)]
    pub worker_id: String,
    #[serde(default)]
    pub queue: String,
    #[serde(default)]
    pub compute_backend: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub hostname: String,
    pub started_at: DateTime<Utc>,
    pub last_heartbeat: DateTime<Utc>,
    #[serde(default)]
    pub items_processed: i64,
    #[serde(default)]
    pub status: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BatchEnqueueItem {
    pub key: String,
    #[serde(default)]
    pub priority: i32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub not_before: Option<DateTime<Utc>>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_status_serde() {
        let s = Status::Pending;
        let json = serde_json::to_string(&s).unwrap();
        assert_eq!(json, r#""pending""#);

        let back: Status = serde_json::from_str(&json).unwrap();
        assert_eq!(back, Status::Pending);
    }

    #[test]
    fn test_status_dead_letter_serde() {
        let s = Status::DeadLetter;
        let json = serde_json::to_string(&s).unwrap();
        assert_eq!(json, r#""dead_letter""#);
    }

    #[test]
    fn test_valid_transitions() {
        assert!(Status::Pending.valid_transition(&Status::Claimed));
        assert!(!Status::Pending.valid_transition(&Status::Running));
        assert!(Status::Running.valid_transition(&Status::Succeeded));
        assert!(Status::Failed.valid_transition(&Status::DeadLetter));
        assert!(Status::DeadLetter.valid_transition(&Status::Pending));
        assert!(!Status::Succeeded.valid_transition(&Status::Pending));
    }

    #[test]
    fn test_work_item_deserialize() {
        let json = r#"{
            "queue": "builds",
            "key": "curl-8.7.1",
            "status": "pending",
            "priority": 10,
            "attempts": 0,
            "max_attempts": 5,
            "created_at": "2026-05-10T12:00:00Z",
            "updated_at": "2026-05-10T12:00:00Z"
        }"#;
        let item: WorkItem = serde_json::from_str(json).unwrap();
        assert_eq!(item.key, "curl-8.7.1");
        assert_eq!(item.status, Status::Pending);
        assert_eq!(item.priority, 10);
    }

    #[test]
    fn test_work_item_omits_empty() {
        let json = r#"{
            "queue": "q",
            "key": "k",
            "status": "pending",
            "created_at": "2026-05-10T12:00:00Z",
            "updated_at": "2026-05-10T12:00:00Z"
        }"#;
        let item: WorkItem = serde_json::from_str(json).unwrap();
        let out = serde_json::to_string(&item).unwrap();
        assert!(!out.contains("worker_id"));
        assert!(!out.contains("not_before"));
    }

    #[test]
    fn test_queue_config_roundtrip() {
        let cfg = QueueConfig {
            max_concurrency: 10,
            max_retry: 5,
            compute_backend: "kubernetes".to_string(),
        };
        let json = serde_json::to_string(&cfg).unwrap();
        let back: QueueConfig = serde_json::from_str(&json).unwrap();
        assert_eq!(back.max_concurrency, 10);
        assert_eq!(back.max_retry, 5);
    }

    #[test]
    fn test_batch_enqueue_item() {
        let item = BatchEnqueueItem {
            key: "pkg-1.0".to_string(),
            priority: 5,
            not_before: None,
        };
        let json = serde_json::to_string(&item).unwrap();
        assert!(!json.contains("not_before"));
    }
}
