use std::path::Path;

use factory_workqueue::duration::parse_duration;
use factory_workqueue::reconciler::{completed, converged, fan_out, requeue_after, ProcessRequest};
use factory_workqueue::types::{Status, WorkItem};

fn fixtures_dir() -> std::path::PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("tests")
        .join("sdk-conformance")
        .join("fixtures")
}

fn load_fixture(name: &str) -> serde_json::Value {
    let path = fixtures_dir().join(name);
    let content = std::fs::read_to_string(&path).unwrap_or_else(|e| {
        panic!("failed to read fixture {}: {e}", path.display());
    });
    serde_json::from_str(&content).unwrap()
}

#[test]
fn test_response_builder_conformance() {
    let fixture = load_fixture("response_builders.json");
    let tests = fixture["tests"].as_array().unwrap();

    for tc in tests {
        let name = tc["name"].as_str().unwrap();
        let builder = tc["builder"].as_str().unwrap();
        let args: Vec<&str> = tc["args"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        let expected = &tc["expected"];

        let resp = match builder {
            "completed" => completed(),
            "converged" => converged(),
            "requeue_after" => {
                let d = parse_duration(args[0]).unwrap();
                requeue_after(d)
            }
            "fan_out" => fan_out(&args),
            _ => panic!("unknown builder: {builder}"),
        };

        let got = serde_json::to_value(&resp).unwrap();

        for (key, want) in expected.as_object().unwrap() {
            assert_eq!(
                got.get(key).unwrap_or(&serde_json::Value::Null),
                want,
                "test {name}: field {key} mismatch"
            );
        }
    }
}

#[test]
fn test_process_request_conformance() {
    let fixture = load_fixture("process_request.json");
    let tests = fixture["tests"].as_array().unwrap();

    for tc in tests {
        let name = tc["name"].as_str().unwrap();
        let req: ProcessRequest = serde_json::from_value(tc["json"].clone()).unwrap();
        let expected = &tc["expected"];

        assert_eq!(req.key, expected["key"].as_str().unwrap(), "test {name}: key");
        assert_eq!(
            req.attempt,
            expected["attempt"].as_i64().unwrap() as i32,
            "test {name}: attempt"
        );
        assert_eq!(
            req.priority,
            expected["priority"].as_i64().unwrap() as i32,
            "test {name}: priority"
        );
        assert_eq!(
            req.trace_id,
            expected["trace_id"].as_str().unwrap(),
            "test {name}: trace_id"
        );
    }
}

#[test]
fn test_status_transition_conformance() {
    let fixture = load_fixture("status_transitions.json");

    let valid = fixture["valid"].as_array().unwrap();
    for tc in valid {
        let from: Status = serde_json::from_value(tc["from"].clone()).unwrap();
        let to: Status = serde_json::from_value(tc["to"].clone()).unwrap();
        assert!(
            from.valid_transition(&to),
            "{:?} -> {:?} should be valid",
            from,
            to
        );
    }

    let invalid = fixture["invalid"].as_array().unwrap();
    for tc in invalid {
        let from: Status = serde_json::from_value(tc["from"].clone()).unwrap();
        let to: Status = serde_json::from_value(tc["to"].clone()).unwrap();
        assert!(
            !from.valid_transition(&to),
            "{:?} -> {:?} should be invalid",
            from,
            to
        );
    }
}

#[test]
fn test_work_item_conformance() {
    let fixture = load_fixture("work_item.json");
    let tests = fixture["tests"].as_array().unwrap();

    for tc in tests {
        let name = tc["name"].as_str().unwrap();
        let item: WorkItem = serde_json::from_value(tc["json"].clone()).unwrap();

        let expected_status: Status =
            serde_json::from_value(serde_json::Value::String(tc["expected_status"].as_str().unwrap().to_string()))
                .unwrap();
        assert_eq!(item.status, expected_status, "test {name}: status");
        assert_eq!(
            item.key,
            tc["expected_key"].as_str().unwrap(),
            "test {name}: key"
        );
        assert_eq!(
            item.priority,
            tc["expected_priority"].as_i64().unwrap() as i32,
            "test {name}: priority"
        );
    }
}
