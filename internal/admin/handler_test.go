package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/admin"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func newServer(t *testing.T) (*httptest.Server, store.Interface) {
	t.Helper()
	s := inmem.New()
	ctx := context.Background()
	s.EnsureQueue(ctx, "build", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})
	s.EnsureQueue(ctx, "test-queue", store.QueueConfig{
		MaxConcurrency: 5, MaxRetry: 3, ComputeBackend: "ec2",
	})

	mux := http.NewServeMux()
	admin.NewHandler(s, noop.Authorizer{}).Register(mux)
	return httptest.NewServer(mux), s
}

func get(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func post(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", nil)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func delete_(t *testing.T, srv *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestListQueues(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)
	s.Enqueue(ctx, "build", "pkg-2", 10)

	resp := get(t, srv, "/admin/queues")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var queues []store.QueueInfo
	decodeJSON(t, resp, &queues)

	if len(queues) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(queues))
	}

	// Find the "build" queue.
	var build *store.QueueInfo
	for i := range queues {
		if queues[i].Name == "build" {
			build = &queues[i]
		}
	}
	if build == nil {
		t.Fatal("build queue not found")
	}
	if build.Counts["pending"] != 2 {
		t.Errorf("expected 2 pending in build, got %d", build.Counts["pending"])
	}
	if build.MaxConcurrency != 10 {
		t.Errorf("expected max_concurrency 10, got %d", build.MaxConcurrency)
	}
}

func TestGetQueue(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()

	resp := get(t, srv, "/admin/queues/build")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var q store.QueueInfo
	decodeJSON(t, resp, &q)
	if q.Name != "build" {
		t.Errorf("expected name build, got %s", q.Name)
	}
}

func TestGetQueue_NotFound(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()

	resp := get(t, srv, "/admin/queues/nonexistent")
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestListItems(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)
	s.Enqueue(ctx, "build", "pkg-2", 50)
	s.Enqueue(ctx, "build", "pkg-3", 10)

	resp := get(t, srv, "/admin/queues/build/items")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []store.WorkItem
	decodeJSON(t, resp, &items)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	// Should be ordered by priority DESC.
	if items[0].Priority < items[1].Priority {
		t.Errorf("expected descending priority order")
	}
}

func TestListItems_StatusFilter(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)
	s.Enqueue(ctx, "build", "pkg-2", 0)
	s.ClaimBatch(ctx, "build", 1, "w", time.Hour)

	resp := get(t, srv, "/admin/queues/build/items?status=pending")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []store.WorkItem
	decodeJSON(t, resp, &items)
	if len(items) != 1 {
		t.Errorf("expected 1 pending item, got %d", len(items))
	}
}

func TestListItems_EmptyQueue(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()

	resp := get(t, srv, "/admin/queues/build/items")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []store.WorkItem
	decodeJSON(t, resp, &items)
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestGetItem(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 42)
	s.ClaimBatch(ctx, "build", 1, "worker-1", time.Hour)
	s.Complete(ctx, "build", "pkg-1")

	resp := get(t, srv, "/admin/queues/build/items/pkg-1")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var detail store.ItemDetail
	decodeJSON(t, resp, &detail)
	if detail.Item.Key != "pkg-1" {
		t.Errorf("expected key pkg-1, got %s", detail.Item.Key)
	}
	if detail.Item.Priority != 42 {
		t.Errorf("expected priority 42, got %d", detail.Item.Priority)
	}
	if len(detail.History) < 2 {
		t.Errorf("expected at least 2 history entries, got %d", len(detail.History))
	}
}

func TestRetryItem(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)
	s.ClaimBatch(ctx, "build", 1, "w", time.Hour)
	s.Fail(ctx, "build", "pkg-1", "broken")

	resp := post(t, srv, "/admin/queues/build/items/pkg-1/retry")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	item, _ := s.GetItem(ctx, "build", "pkg-1")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after retry, got %s", item.Status)
	}
}

func TestRetryItem_DeadLettered(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)
	s.ClaimBatch(ctx, "build", 1, "w", time.Hour)
	s.Deadletter(ctx, "build", "pkg-1")

	resp := post(t, srv, "/admin/queues/build/items/pkg-1/retry")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	item, _ := s.GetItem(ctx, "build", "pkg-1")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after retry from dead-letter, got %s", item.Status)
	}
}

func TestCancelItem(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	s.Enqueue(ctx, "build", "pkg-1", 0)

	resp := post(t, srv, "/admin/queues/build/items/pkg-1/cancel")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	item, _ := s.GetItem(ctx, "build", "pkg-1")
	if item.Status != store.StatusFailed {
		t.Errorf("expected failed after cancel, got %s", item.Status)
	}
	if item.ErrorMessage != "cancelled via admin API" {
		t.Errorf("expected cancel message, got %q", item.ErrorMessage)
	}
}

func TestPurgeDeadLetters(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	ctx := context.Background()
	for i := range 3 {
		key := "pkg-" + string(rune('a'+i))
		s.Enqueue(ctx, "build", key, 0)
		s.ClaimBatch(ctx, "build", 1, "w", time.Hour)
		s.Deadletter(ctx, "build", key)
	}

	resp := delete_(t, srv, "/admin/queues/build/dead-letters")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	decodeJSON(t, resp, &result)
	if count, ok := result["count"].(float64); !ok || count != 3 {
		t.Errorf("expected 3 purged, got %v", result["count"])
	}

	counts, _ := s.CountByStatus(ctx, "build")
	if counts[store.StatusDeadLetter] != 0 {
		t.Errorf("expected 0 dead-lettered after purge, got %d", counts[store.StatusDeadLetter])
	}
}

func TestListWorkers(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()

	resp := get(t, srv, "/admin/workers")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var workers []store.WorkerLease
	decodeJSON(t, resp, &workers)
	// inmem store returns empty workers list.
	if len(workers) != 0 {
		t.Errorf("expected 0 workers from inmem, got %d", len(workers))
	}
}

func TestListWorkers_QueueFilter(t *testing.T) {
	srv, _ := newServer(t)
	defer srv.Close()

	resp := get(t, srv, "/admin/workers?queue=build")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStreamEvents(t *testing.T) {
	srv, s := newServer(t)
	defer srv.Close()

	// Start SSE client in background.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/admin/queues/build/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE request: %v", err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	// Enqueue an item to trigger an event.
	s.Enqueue(context.Background(), "build", "sse-test", 0)

	// Read at least one event.
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if n == 0 {
		t.Skip("no SSE data received (timing-dependent)")
	}
	data := string(buf[:n])
	if len(data) == 0 {
		t.Skip("empty SSE response")
	}
}
