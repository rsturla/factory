use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::duration::format_duration;

pub const ACTION_COMPLETED: &str = "completed";
pub const ACTION_CONVERGED: &str = "converged";
pub const ACTION_REQUEUE: &str = "requeue";
pub const ACTION_FAN_OUT: &str = "fan_out";

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProcessRequest {
    pub key: String,
    #[serde(default)]
    pub attempt: i32,
    #[serde(default)]
    pub priority: i32,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub trace_id: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProcessResponse {
    pub action: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub requeue_after: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub fan_out_keys: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
}

pub fn completed() -> ProcessResponse {
    ProcessResponse {
        action: ACTION_COMPLETED.to_string(),
        requeue_after: String::new(),
        fan_out_keys: Vec::new(),
        error: String::new(),
    }
}

pub fn converged() -> ProcessResponse {
    ProcessResponse {
        action: ACTION_CONVERGED.to_string(),
        requeue_after: String::new(),
        fan_out_keys: Vec::new(),
        error: String::new(),
    }
}

pub fn requeue_after(delay: Duration) -> ProcessResponse {
    ProcessResponse {
        action: ACTION_REQUEUE.to_string(),
        requeue_after: format_duration(delay),
        fan_out_keys: Vec::new(),
        error: String::new(),
    }
}

pub fn fan_out(keys: &[&str]) -> ProcessResponse {
    if keys.is_empty() {
        return completed();
    }
    ProcessResponse {
        action: ACTION_FAN_OUT.to_string(),
        requeue_after: String::new(),
        fan_out_keys: keys.iter().map(|s| s.to_string()).collect(),
        error: String::new(),
    }
}

pub type ReconcileFunc =
    Arc<dyn Fn(ProcessRequest) -> Result<ProcessResponse, Box<dyn std::error::Error + Send + Sync>> + Send + Sync>;

/// Framework-agnostic request processor. Takes raw JSON bytes, returns (status_code, response_bytes).
pub fn process(f: &ReconcileFunc, body: &[u8]) -> (u16, Vec<u8>) {
    let req: ProcessRequest = match serde_json::from_slice(body) {
        Ok(r) => r,
        Err(e) => {
            let msg = format!("invalid request body: {e}");
            return (400, msg.into_bytes());
        }
    };

    let resp = match f(req) {
        Ok(r) => r,
        Err(e) => ProcessResponse {
            action: String::new(),
            requeue_after: String::new(),
            fan_out_keys: Vec::new(),
            error: e.to_string(),
        },
    };

    let body = serde_json::to_vec(&resp).unwrap_or_default();
    (200, body)
}

#[cfg(feature = "axum-handler")]
mod axum_handler {
    use super::*;
    use axum::extract::State;
    use axum::http::StatusCode;
    use axum::response::IntoResponse;
    use axum::routing::post;
    use axum::{Json, Router};

    async fn process_handler(
        State(f): State<ReconcileFunc>,
        body: axum::body::Bytes,
    ) -> impl IntoResponse {
        let (status, resp_body) = process(&f, &body);
        let status_code = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);

        if status == 200 {
            let resp: ProcessResponse = serde_json::from_slice(&resp_body).unwrap();
            (status_code, Json(resp)).into_response()
        } else {
            (status_code, String::from_utf8_lossy(&resp_body).to_string()).into_response()
        }
    }

    pub fn reconciler_handler(f: ReconcileFunc) -> Router {
        Router::new()
            .route("/process", post(process_handler))
            .with_state(f)
    }

    pub fn serve(f: ReconcileFunc, addr: &str) {
        let rt = tokio::runtime::Runtime::new().expect("failed to create tokio runtime");
        rt.block_on(async {
            let app = reconciler_handler(f);
            let listener = tokio::net::TcpListener::bind(addr).await.unwrap();
            axum::serve(listener, app).await.unwrap();
        });
    }
}

#[cfg(feature = "axum-handler")]
pub use axum_handler::{reconciler_handler, serve};

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_completed() {
        let r = completed();
        assert_eq!(r.action, ACTION_COMPLETED);
        let json = serde_json::to_value(&r).unwrap();
        assert_eq!(json, serde_json::json!({"action": "completed"}));
    }

    #[test]
    fn test_converged() {
        let r = converged();
        assert_eq!(r.action, ACTION_CONVERGED);
    }

    #[test]
    fn test_requeue_after() {
        let r = requeue_after(Duration::from_secs(30));
        assert_eq!(r.action, ACTION_REQUEUE);
        assert_eq!(r.requeue_after, "30s");
    }

    #[test]
    fn test_requeue_after_5m() {
        let r = requeue_after(Duration::from_secs(300));
        assert_eq!(r.requeue_after, "5m0s");
    }

    #[test]
    fn test_fan_out() {
        let r = fan_out(&["a", "b", "c"]);
        assert_eq!(r.action, ACTION_FAN_OUT);
        assert_eq!(r.fan_out_keys, vec!["a", "b", "c"]);
    }

    #[test]
    fn test_fan_out_empty() {
        let r = fan_out(&[]);
        assert_eq!(r.action, ACTION_COMPLETED);
        assert!(r.fan_out_keys.is_empty());
    }

    #[test]
    fn test_process_request_deserialize() {
        let json = r#"{"key": "curl-1.0", "attempt": 2, "priority": 5, "trace_id": "abc"}"#;
        let req: ProcessRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.key, "curl-1.0");
        assert_eq!(req.attempt, 2);
        assert_eq!(req.priority, 5);
        assert_eq!(req.trace_id, "abc");
    }

    #[test]
    fn test_process_request_minimal() {
        let json = r#"{"key": "k"}"#;
        let req: ProcessRequest = serde_json::from_str(json).unwrap();
        assert_eq!(req.key, "k");
        assert_eq!(req.attempt, 0);
        assert_eq!(req.priority, 0);
        assert_eq!(req.trace_id, "");
    }

    #[test]
    fn test_response_omits_empty() {
        let r = completed();
        let json = serde_json::to_string(&r).unwrap();
        assert!(!json.contains("requeue_after"));
        assert!(!json.contains("fan_out_keys"));
        assert!(!json.contains("error"));
    }

    #[test]
    fn test_process_fn_completed() {
        let f: ReconcileFunc = Arc::new(|_| Ok(completed()));
        let body = br#"{"key": "test"}"#;
        let (status, resp) = process(&f, body);
        assert_eq!(status, 200);
        let r: ProcessResponse = serde_json::from_slice(&resp).unwrap();
        assert_eq!(r.action, ACTION_COMPLETED);
    }

    #[test]
    fn test_process_fn_bad_json() {
        let f: ReconcileFunc = Arc::new(|_| Ok(completed()));
        let (status, _) = process(&f, b"not json");
        assert_eq!(status, 400);
    }

    #[test]
    fn test_process_fn_error() {
        let f: ReconcileFunc = Arc::new(|_| Err("boom".into()));
        let body = br#"{"key": "k"}"#;
        let (status, resp) = process(&f, body);
        assert_eq!(status, 200);
        let r: ProcessResponse = serde_json::from_slice(&resp).unwrap();
        assert_eq!(r.error, "boom");
    }
}
