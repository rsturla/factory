package conformance_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/types"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../fixtures/" + name)
	if err != nil {
		t.Fatalf("load fixture %s: %v", name, err)
	}
	return data
}

func TestResponseBuilderConformance(t *testing.T) {
	data := loadFixture(t, "response_builders.json")
	var fixture struct {
		Tests []struct {
			Name     string          `json:"name"`
			Builder  string          `json:"builder"`
			Args     []string        `json:"args"`
			Expected json.RawMessage `json:"expected"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	for _, tc := range fixture.Tests {
		t.Run(tc.Name, func(t *testing.T) {
			var resp reconciler.ProcessResponse

			switch tc.Builder {
			case "completed":
				resp = reconciler.Completed()
			case "converged":
				resp = reconciler.Converged()
			case "requeue_after":
				d, err := time.ParseDuration(tc.Args[0])
				if err != nil {
					t.Fatalf("parse duration %q: %v", tc.Args[0], err)
				}
				resp = reconciler.RequeueAfter(d)
			case "fan_out":
				resp = reconciler.FanOut(tc.Args...)
			default:
				t.Fatalf("unknown builder: %s", tc.Builder)
			}

			got, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}

			var gotMap, expectedMap map[string]any
			json.Unmarshal(got, &gotMap)
			json.Unmarshal(tc.Expected, &expectedMap)

			for key, want := range expectedMap {
				gotVal, ok := gotMap[key]
				if !ok {
					t.Errorf("missing field %q", key)
					continue
				}
				wantJSON, _ := json.Marshal(want)
				gotJSON, _ := json.Marshal(gotVal)
				if string(wantJSON) != string(gotJSON) {
					t.Errorf("field %q: got %s, want %s", key, gotJSON, wantJSON)
				}
			}
		})
	}
}

func TestProcessRequestConformance(t *testing.T) {
	data := loadFixture(t, "process_request.json")
	var fixture struct {
		Tests []struct {
			Name     string                    `json:"name"`
			JSON     json.RawMessage           `json:"json"`
			Expected reconciler.ProcessRequest `json:"expected"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	for _, tc := range fixture.Tests {
		t.Run(tc.Name, func(t *testing.T) {
			var req reconciler.ProcessRequest
			if err := json.Unmarshal(tc.JSON, &req); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if req.Key != tc.Expected.Key {
				t.Errorf("key: got %q, want %q", req.Key, tc.Expected.Key)
			}
			if req.Attempt != tc.Expected.Attempt {
				t.Errorf("attempt: got %d, want %d", req.Attempt, tc.Expected.Attempt)
			}
			if req.Priority != tc.Expected.Priority {
				t.Errorf("priority: got %d, want %d", req.Priority, tc.Expected.Priority)
			}
			if req.TraceID != tc.Expected.TraceID {
				t.Errorf("trace_id: got %q, want %q", req.TraceID, tc.Expected.TraceID)
			}
		})
	}
}

func TestStatusTransitionConformance(t *testing.T) {
	data := loadFixture(t, "status_transitions.json")
	var fixture struct {
		Valid   []struct{ From, To string } `json:"valid"`
		Invalid []struct{ From, To string } `json:"invalid"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	for _, tc := range fixture.Valid {
		t.Run(tc.From+"_to_"+tc.To+"_valid", func(t *testing.T) {
			if !types.ValidTransition(types.Status(tc.From), types.Status(tc.To)) {
				t.Errorf("%s -> %s should be valid", tc.From, tc.To)
			}
		})
	}

	for _, tc := range fixture.Invalid {
		t.Run(tc.From+"_to_"+tc.To+"_invalid", func(t *testing.T) {
			if types.ValidTransition(types.Status(tc.From), types.Status(tc.To)) {
				t.Errorf("%s -> %s should be invalid", tc.From, tc.To)
			}
		})
	}
}

func TestWorkItemConformance(t *testing.T) {
	data := loadFixture(t, "work_item.json")
	var fixture struct {
		Tests []struct {
			Name             string          `json:"name"`
			JSON             json.RawMessage `json:"json"`
			ExpectedStatus   string          `json:"expected_status"`
			ExpectedKey      string          `json:"expected_key"`
			ExpectedPriority int             `json:"expected_priority"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	for _, tc := range fixture.Tests {
		t.Run(tc.Name, func(t *testing.T) {
			var item types.WorkItem
			if err := json.Unmarshal(tc.JSON, &item); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if string(item.Status) != tc.ExpectedStatus {
				t.Errorf("status: got %q, want %q", item.Status, tc.ExpectedStatus)
			}
			if item.Key != tc.ExpectedKey {
				t.Errorf("key: got %q, want %q", item.Key, tc.ExpectedKey)
			}
			if item.Priority != tc.ExpectedPriority {
				t.Errorf("priority: got %d, want %d", item.Priority, tc.ExpectedPriority)
			}
		})
	}
}
