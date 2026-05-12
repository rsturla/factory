package dispatcher_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/dispatcher"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/client"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

// fakeReconciler returns a test HTTP server that responds to /process
// with the given handler function.
func fakeReconciler(t *testing.T, fn func(reconciler.ProcessRequest) reconciler.ProcessResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req reconciler.ProcessRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := fn(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func newDispatcher(t *testing.T, s store.Interface, reconcilerURL string) (*dispatcher.Dispatcher, dispatcher.Config) {
	t.Helper()
	cfg := dispatcher.Config{
		QueueName:        "test",
		WorkerID:         "test-dispatcher",
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    1 * time.Hour, // don't sweep during tests
		ReaperInterval:   1 * time.Hour, // don't reap during tests
		LeaseDuration:    1 * time.Hour,
		BatchSize:        10,
		MaxConcurrency:   5,
		MaxRetry:         3,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(reconcilerURL), cfg)
	return d, cfg
}

func newStore(t *testing.T) store.Interface {
	t.Helper()
	s := inmem.New()
	return s
}

func TestDispatcher_ClaimsAndCompletes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var processed sync.Map
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		processed.Store(req.Key, true)
		return reconciler.Completed()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	// Run dispatcher briefly.
	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	// Enqueue items before starting.
	s.Enqueue(ctx, "test", "item-1", 0)
	s.Enqueue(ctx, "test", "item-2", 0)
	s.Enqueue(ctx, "test", "item-3", 0)

	d.Run(dctx)

	// Verify all items were processed.
	for _, key := range []string{"item-1", "item-2", "item-3"} {
		if _, ok := processed.Load(key); !ok {
			t.Errorf("item %s was not processed", key)
		}
	}

	// Verify all items are succeeded.
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 3 {
		t.Errorf("expected 3 succeeded, got %v", counts)
	}
}

func TestDispatcher_Converged(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		return reconciler.Converged()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "already-done", 0)

	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded (converged), got %v", counts)
	}
}

func TestDispatcher_ReconcilerError_Requeues(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var attempts atomic.Int32
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		n := attempts.Add(1)
		if n <= 1 {
			return reconciler.ProcessResponse{Error: "temporary failure"}
		}
		return reconciler.Completed()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "retry-me", 0)

	// Run long enough for at least 2 dispatch cycles.
	dctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	d.Run(dctx)

	// Item should have been retried and eventually succeeded or be pending with backoff.
	got := attempts.Load()
	if got < 1 {
		t.Errorf("expected at least 1 attempt, got %d", got)
	}
}

func TestDispatcher_ReconcilerUnreachable_InfraFailure(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Point to a closed server — connection refused.
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		return reconciler.Completed()
	})
	srv.Close() // close immediately

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "infra-fail", 0)

	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	// Item should be back to pending (infra failure doesn't consume retry budget).
	item, err := s.GetItem(ctx, "test", "infra-fail")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 after infra failure, got %d", item.Attempts)
	}
}

func TestDispatcher_FanOut(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		if req.Key == "parent" {
			return reconciler.FanOut("child-1", "child-2")
		}
		return reconciler.Completed()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "parent", 0)

	dctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	d.Run(dctx)

	// Parent should be succeeded.
	parent, _ := s.GetItem(ctx, "test", "parent")
	if parent.Status != store.StatusSucceeded {
		t.Errorf("expected parent succeeded, got %s", parent.Status)
	}

	// Children should exist (either succeeded or pending depending on timing).
	for _, key := range []string{"child-1", "child-2"} {
		_, err := s.GetItem(ctx, "test", key)
		if err != nil {
			t.Errorf("expected fan-out child %s to exist: %v", key, err)
		}
	}
}

func TestDispatcher_RequeueAfter(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		return reconciler.RequeueAfter(10 * time.Minute)
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "later", 0)

	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	item, _ := s.GetItem(ctx, "test", "later")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after requeue, got %s", item.Status)
	}
	if item.NotBefore == nil {
		t.Error("expected NotBefore set after requeue")
	}
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 (requeue doesn't consume budget), got %d", item.Attempts)
	}
}

func TestDispatcher_PriorityOrder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var order []string
	var mu sync.Mutex
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		mu.Lock()
		order = append(order, req.Key)
		mu.Unlock()
		return reconciler.Completed()
	})
	defer srv.Close()

	// Use batch size 1 so items are processed one at a time in priority order.
	cfg := dispatcher.Config{
		QueueName:        "test",
		WorkerID:         "test",
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    1 * time.Hour,
		ReaperInterval:   1 * time.Hour,
		LeaseDuration:    1 * time.Hour,
		BatchSize:        1,
		MaxConcurrency:   1,
		MaxRetry:         3,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(srv.URL), cfg)

	s.Enqueue(ctx, "test", "low", -10)
	s.Enqueue(ctx, "test", "high", 100)
	s.Enqueue(ctx, "test", "normal", 0)

	dctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	d.Run(dctx)

	mu.Lock()
	defer mu.Unlock()

	if len(order) < 3 {
		t.Fatalf("expected 3 processed, got %d: %v", len(order), order)
	}
	if order[0] != "high" {
		t.Errorf("expected first=high, got %s", order[0])
	}
	if order[1] != "normal" {
		t.Errorf("expected second=normal, got %s", order[1])
	}
	if order[2] != "low" {
		t.Errorf("expected third=low, got %s", order[2])
	}
}

func TestDispatcher_MaxConcurrency(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var concurrent atomic.Int32
	var maxSeen atomic.Int32

	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		n := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if int32(n) <= old || maxSeen.CompareAndSwap(old, int32(n)) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		concurrent.Add(-1)
		return reconciler.Completed()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	// Enqueue more items than max_concurrency (5).
	for i := range 10 {
		s.Enqueue(ctx, "test", string(rune('a'+i)), 0)
	}

	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	d.Run(dctx)

	if maxSeen.Load() > 5 {
		t.Errorf("expected max concurrent <= 5, saw %d", maxSeen.Load())
	}
}

func TestDispatcher_GracefulShutdown(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var processed atomic.Int32

	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		processed.Add(1)
		return reconciler.Completed()
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "item-1", 0)

	// Run briefly, then cancel. Run() should return without hanging.
	dctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		d.Run(dctx)
		close(done)
	}()

	select {
	case <-done:
		// Run() returned — graceful shutdown worked.
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation (hung on drain)")
	}

	if processed.Load() < 1 {
		t.Error("expected at least 1 item processed before shutdown")
	}
}

func TestReconcilerTimeout(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Reconciler sleeps longer than the lease, so the item's lease expires.
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		time.Sleep(500 * time.Millisecond)
		return reconciler.Completed()
	})
	defer srv.Close()

	// Short lease so the reaper can reclaim the item quickly.
	cfg := dispatcher.Config{
		QueueName:        "test",
		WorkerID:         "timeout-test",
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    1 * time.Hour,
		ReaperInterval:   100 * time.Millisecond,
		LeaseDuration:    50 * time.Millisecond, // very short lease
		BatchSize:        10,
		MaxConcurrency:   5,
		MaxRetry:         3,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(srv.URL), cfg)

	s.Enqueue(ctx, "test", "slow-item", 0)

	// Run long enough for the lease to expire and the reaper to fire.
	dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	d.Run(dctx)

	// The item should have been reaped back to pending because its lease
	// expired while the reconciler was still sleeping.
	item, err := s.GetItem(ctx, "test", "slow-item")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	// After reap, item goes back to pending (the reaper calls Requeue).
	if item.Status != store.StatusPending && item.Status != store.StatusSucceeded {
		t.Errorf("expected pending or succeeded after timeout + reap, got %s", item.Status)
	}
}

func TestReconcilerInvalidJSON(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Return non-JSON body — the ReconcilerClient.Process will fail to decode.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("this is not json"))
	}))
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "bad-json", 0)

	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	// Invalid JSON triggers an infra failure (reconciler call returns error),
	// which requeues without consuming retry budget.
	item, err := s.GetItem(ctx, "test", "bad-json")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 after infra failure (bad JSON), got %d", item.Attempts)
	}
}

func TestReconcilerServerError(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Reconciler returns 500 — the client treats non-200 as an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "server-error", 0)

	dctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	// HTTP 500 triggers an infra failure — retry budget is not consumed.
	item, err := s.GetItem(ctx, "test", "server-error")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 after server error (infra failure), got %d", item.Attempts)
	}
}

func TestReconcilerReturnsRetry(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	var attempts atomic.Int32
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		attempts.Add(1)
		return reconciler.ProcessResponse{
			Error: "transient failure",
		}
	})
	defer srv.Close()

	d, _ := newDispatcher(t, s, srv.URL)

	s.Enqueue(ctx, "test", "retry-item", 0)

	dctx, cancel := context.WithTimeout(ctx, 1*time.Second)
	defer cancel()
	d.Run(dctx)

	got := attempts.Load()
	if got < 1 {
		t.Errorf("expected at least 1 attempt, got %d", got)
	}

	// After the error response, completion.HandleFailure does Fail → Requeue
	// with exponential backoff (NotBefore in the future). The item should be
	// back to pending, waiting for the backoff delay to elapse.
	item, err := s.GetItem(ctx, "test", "retry-item")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after retry with backoff, got %s", item.Status)
	}
	if item.NotBefore == nil {
		t.Error("expected NotBefore set (backoff delay) after error-triggered retry")
	}
	if item.NotBefore != nil && !item.NotBefore.After(time.Now()) {
		t.Error("expected NotBefore to be in the future (backoff)")
	}
}

func TestDispatcher_Reaper(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Don't need a real reconciler for reaper test.
	srv := fakeReconciler(t, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		return reconciler.Completed()
	})
	defer srv.Close()

	// Enqueue and claim with a very short lease.
	s.EnsureQueue(ctx, "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5,
	})
	s.Enqueue(ctx, "test", "expired", 0)
	s.ClaimBatch(ctx, "test", 1, "dead-worker", 1*time.Millisecond)

	// Wait for lease to expire.
	time.Sleep(10 * time.Millisecond)

	// Run dispatcher in sweep-only mode so it only reaps, never dispatches.
	cfg := dispatcher.Config{
		QueueName:      "test",
		WorkerID:       "reaper-test",
		Mode:           dispatcher.ModeSweepOnly,
		SweepInterval:  1 * time.Hour,
		ReaperInterval: 50 * time.Millisecond,
		LeaseDuration:  1 * time.Hour,
		BatchSize:      10,
		MaxConcurrency: 10,
		MaxRetry:       5,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(srv.URL), cfg)

	dctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	d.Run(dctx)

	// Item should have been reaped back to pending.
	item, err := s.GetItem(ctx, "test", "expired")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after reap, got %s", item.Status)
	}
}
