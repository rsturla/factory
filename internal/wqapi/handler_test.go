package wqapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func setupHandler(t *testing.T) (*Handler, store.Interface) {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 3})
	h := NewHandler(s, noop.Authorizer{})
	return h, s
}

func newTestMux(t *testing.T) (http.Handler, store.Interface) {
	t.Helper()
	h, s := setupHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux, s
}

func postJSON(t *testing.T, handler http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func decodeJSON(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(v); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
}

// enqueueItem is a test helper that enqueues a single item via the API.
func enqueueItem(t *testing.T, mux http.Handler, queue, key string, priority int) {
	t.Helper()
	rr := postJSON(t, mux, "/wq/enqueue", map[string]any{
		"queue":    queue,
		"key":      key,
		"priority": priority,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("enqueue %s/%s: want 200, got %d: %s", queue, key, rr.Code, rr.Body.String())
	}
}

// claimItem is a test helper that claims a single item via the API.
func claimItem(t *testing.T, mux http.Handler, queue string) store.WorkItem {
	t.Helper()
	rr := postJSON(t, mux, "/wq/claim", map[string]any{
		"queue":          queue,
		"batch_size":     1,
		"worker_id":      "test-worker",
		"lease_duration": "1h",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("claim from %s: want 200, got %d: %s", queue, rr.Code, rr.Body.String())
	}
	var items []store.WorkItem
	decodeJSON(t, rr, &items)
	if len(items) == 0 {
		t.Fatalf("claim from %s: expected 1 item, got 0", queue)
	}
	return items[0]
}

func TestEnqueue(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/enqueue", map[string]any{
		"queue":    "test",
		"key":      "item-1",
		"priority": 5,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	var resp map[string]string
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %q", resp["status"])
	}
}

func TestEnqueueBatch(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/enqueue-batch", map[string]any{
		"queue": "test",
		"items": []map[string]any{
			{"key": "b1", "priority": 1},
			{"key": "b2", "priority": 2},
			{"key": "b3", "priority": 3},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %v", resp["status"])
	}
	count, ok := resp["count"].(float64)
	if !ok || int(count) != 3 {
		t.Fatalf("want count 3, got %v", resp["count"])
	}
}

func TestClaimBatch(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "c1", 1)
	enqueueItem(t, mux, "test", "c2", 2)

	rr := postJSON(t, mux, "/wq/claim", map[string]any{
		"queue":          "test",
		"batch_size":     5,
		"worker_id":      "w1",
		"lease_duration": "30m",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	var items []store.WorkItem
	decodeJSON(t, rr, &items)
	if len(items) != 2 {
		t.Fatalf("want 2 claimed items, got %d", len(items))
	}
	for _, item := range items {
		if item.Status != store.StatusClaimed {
			t.Errorf("claimed item %s has status %q, want %q", item.Key, item.Status, store.StatusClaimed)
		}
		if item.WorkerID != "w1" {
			t.Errorf("claimed item %s has worker %q, want %q", item.Key, item.WorkerID, "w1")
		}
	}
}

func TestClaimBatchEmpty(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/claim", map[string]any{
		"queue":          "test",
		"batch_size":     5,
		"worker_id":      "w1",
		"lease_duration": "1h",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	// Must return [] not null.
	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Fatalf("want empty JSON array [], got %q", body)
	}
}

func TestComplete(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "done-1", 1)
	claimItem(t, mux, "test")

	rr := postJSON(t, mux, "/wq/complete", map[string]any{
		"queue": "test",
		"key":   "done-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %q", resp["status"])
	}
}

func TestCompleteNotFound(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/complete", map[string]any{
		"queue": "test",
		"key":   "nonexistent",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFail(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "fail-1", 1)
	claimItem(t, mux, "test")

	rr := postJSON(t, mux, "/wq/fail", map[string]any{
		"queue": "test",
		"key":   "fail-1",
		"error": "something broke",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %q", resp["status"])
	}

	// Verify the item is now failed via get-item.
	rr = postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "fail-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("get-item: want 200, got %d", rr.Code)
	}
	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.Status != store.StatusFailed {
		t.Fatalf("want status failed, got %q", item.Status)
	}
	if item.ErrorMessage != "something broke" {
		t.Fatalf("want error message %q, got %q", "something broke", item.ErrorMessage)
	}
}

func TestHeartbeat(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "hb-1", 1)
	claimed := claimItem(t, mux, "test")

	rr := postJSON(t, mux, "/wq/heartbeat", map[string]any{
		"queue":    "test",
		"key":      "hb-1",
		"duration": "2h",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify lease was extended.
	rr = postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "hb-1",
	})
	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.LeaseExpires == nil {
		t.Fatal("lease_expires is nil after heartbeat")
	}
	if claimed.LeaseExpires != nil && !item.LeaseExpires.After(*claimed.LeaseExpires) {
		t.Fatal("lease was not extended")
	}
}

func TestTransition(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "tr-1", 1)
	claimItem(t, mux, "test")

	// Transition claimed -> running is valid.
	rr := postJSON(t, mux, "/wq/transition", map[string]any{
		"queue": "test",
		"key":   "tr-1",
		"from":  "claimed",
		"to":    "running",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify item is now running.
	rr = postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "tr-1",
	})
	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.Status != store.StatusRunning {
		t.Fatalf("want status running, got %q", item.Status)
	}
}

func TestTransitionInvalid(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "tr-inv-1", 1)

	// pending -> running is not a valid transition.
	rr := postJSON(t, mux, "/wq/transition", map[string]any{
		"queue": "test",
		"key":   "tr-inv-1",
		"from":  "pending",
		"to":    "running",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestTransitionConflict(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "tr-conf-1", 1)
	claimItem(t, mux, "test")

	// First transition: claimed -> running (succeeds).
	rr := postJSON(t, mux, "/wq/transition", map[string]any{
		"queue": "test",
		"key":   "tr-conf-1",
		"from":  "claimed",
		"to":    "running",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("first transition: want 200, got %d", rr.Code)
	}

	// Second transition: claimed -> running (conflicts, item is now running).
	rr = postJSON(t, mux, "/wq/transition", map[string]any{
		"queue": "test",
		"key":   "tr-conf-1",
		"from":  "claimed",
		"to":    "running",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("second transition: want 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestRequeue(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "rq-1", 1)
	claimItem(t, mux, "test")

	// Fail first, then requeue (requeue requires claimed/running/failed status).
	postJSON(t, mux, "/wq/fail", map[string]any{
		"queue": "test",
		"key":   "rq-1",
		"error": "transient error",
	})

	rr := postJSON(t, mux, "/wq/requeue", map[string]any{
		"queue": "test",
		"key":   "rq-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify item is back to pending.
	rr = postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "rq-1",
	})
	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.Status != store.StatusPending {
		t.Fatalf("want status pending after requeue, got %q", item.Status)
	}
}

func TestDeadletter(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "dl-1", 1)
	claimItem(t, mux, "test")

	// Fail first, then deadletter.
	postJSON(t, mux, "/wq/fail", map[string]any{
		"queue": "test",
		"key":   "dl-1",
		"error": "permanent error",
	})

	rr := postJSON(t, mux, "/wq/deadletter", map[string]any{
		"queue": "test",
		"key":   "dl-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify item is dead-lettered.
	rr = postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "dl-1",
	})
	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.Status != store.StatusDeadLetter {
		t.Fatalf("want status dead_letter, got %q", item.Status)
	}
}

func TestCountByStatus(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "cnt-1", 1)
	enqueueItem(t, mux, "test", "cnt-2", 2)
	enqueueItem(t, mux, "test", "cnt-3", 3)

	// Claim one item to make it "claimed".
	claimItem(t, mux, "test")

	rr := postJSON(t, mux, "/wq/count", map[string]any{
		"queue": "test",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var counts map[string]float64
	decodeJSON(t, rr, &counts)
	if counts["pending"] != 2 {
		t.Errorf("want 2 pending, got %v", counts["pending"])
	}
	if counts["claimed"] != 1 {
		t.Errorf("want 1 claimed, got %v", counts["claimed"])
	}
}

func TestGetItem(t *testing.T) {
	mux, _ := newTestMux(t)

	enqueueItem(t, mux, "test", "get-1", 7)

	rr := postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "get-1",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var item store.WorkItem
	decodeJSON(t, rr, &item)
	if item.Queue != "test" {
		t.Errorf("want queue test, got %q", item.Queue)
	}
	if item.Key != "get-1" {
		t.Errorf("want key get-1, got %q", item.Key)
	}
	if item.Priority != 7 {
		t.Errorf("want priority 7, got %d", item.Priority)
	}
	if item.Status != store.StatusPending {
		t.Errorf("want status pending, got %q", item.Status)
	}
}

func TestGetItemNotFound(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/get-item", map[string]any{
		"queue": "test",
		"key":   "does-not-exist",
	})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestListQueues(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/list-queues", map[string]any{})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var queues []store.QueueInfo
	decodeJSON(t, rr, &queues)
	if len(queues) == 0 {
		t.Fatal("expected at least one queue")
	}

	found := false
	for _, q := range queues {
		if q.Name == "test" {
			found = true
			if q.MaxConcurrency != 10 {
				t.Errorf("want max_concurrency 10, got %d", q.MaxConcurrency)
			}
			if q.MaxRetry != 3 {
				t.Errorf("want max_retry 3, got %d", q.MaxRetry)
			}
		}
	}
	if !found {
		t.Fatal("queue 'test' not found in list-queues response")
	}
}

func TestEnsureQueue(t *testing.T) {
	mux, _ := newTestMux(t)

	rr := postJSON(t, mux, "/wq/ensure-queue", map[string]any{
		"queue": "new-queue",
		"config": map[string]any{
			"max_concurrency": 20,
			"max_retry":       5,
			"compute_backend": "kubernetes",
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]string
	decodeJSON(t, rr, &resp)
	if resp["status"] != "ok" {
		t.Fatalf("want status ok, got %q", resp["status"])
	}

	// Verify via list-queues.
	rr = postJSON(t, mux, "/wq/list-queues", map[string]any{})
	var queues []store.QueueInfo
	decodeJSON(t, rr, &queues)
	found := false
	for _, q := range queues {
		if q.Name == "new-queue" {
			found = true
			if q.MaxConcurrency != 20 {
				t.Errorf("want max_concurrency 20, got %d", q.MaxConcurrency)
			}
			if q.MaxRetry != 5 {
				t.Errorf("want max_retry 5, got %d", q.MaxRetry)
			}
			if q.ComputeBackend != "kubernetes" {
				t.Errorf("want compute_backend kubernetes, got %q", q.ComputeBackend)
			}
		}
	}
	if !found {
		t.Fatal("queue 'new-queue' not found after ensure-queue")
	}
}

func TestSetPaused(t *testing.T) {
	mux, _ := newTestMux(t)

	// Pause the queue.
	rr := postJSON(t, mux, "/wq/set-paused", map[string]any{
		"queue":  "test",
		"paused": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("set-paused: want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify via is-paused.
	rr = postJSON(t, mux, "/wq/is-paused", map[string]any{
		"queue": "test",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("is-paused: want 200, got %d", rr.Code)
	}

	var pauseResp map[string]any
	decodeJSON(t, rr, &pauseResp)
	paused, ok := pauseResp["paused"].(bool)
	if !ok || !paused {
		t.Fatalf("want paused=true, got %v", pauseResp["paused"])
	}

	// Unpause.
	rr = postJSON(t, mux, "/wq/set-paused", map[string]any{
		"queue":  "test",
		"paused": false,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("set-paused (unpause): want 200, got %d", rr.Code)
	}

	rr = postJSON(t, mux, "/wq/is-paused", map[string]any{
		"queue": "test",
	})
	decodeJSON(t, rr, &pauseResp)
	paused, ok = pauseResp["paused"].(bool)
	if !ok || paused {
		t.Fatalf("want paused=false, got %v", pauseResp["paused"])
	}
}

func TestInvalidJSON(t *testing.T) {
	mux, _ := newTestMux(t)

	endpoints := []string{
		"/wq/enqueue",
		"/wq/enqueue-batch",
		"/wq/claim",
		"/wq/complete",
		"/wq/fail",
		"/wq/heartbeat",
		"/wq/transition",
		"/wq/requeue",
		"/wq/requeue-undo",
		"/wq/deadletter",
		"/wq/count",
		"/wq/list",
		"/wq/get-item",
		"/wq/ensure-queue",
		"/wq/set-paused",
		"/wq/is-paused",
		"/wq/record-history",
		"/wq/repair",
		"/wq/purge-dead-letters",
	}

	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			req := httptest.NewRequest("POST", ep, strings.NewReader("{not-json!!}"))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("want 400 for invalid JSON on %s, got %d", ep, rr.Code)
			}
		})
	}
}
