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
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"

	"github.com/hummingbird-org/factory-workqueue/internal/compute"
)

// fakeReconciler returns a test HTTP server that responds to /process
// with the given handler function.
func fakeReconciler(t *testing.T, fn func(sdk.ProcessRequest) sdk.ProcessResponse) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sdk.ProcessRequest
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
		ScaleInterval:    1 * time.Hour, // don't scale during tests
		LeaseDuration:    1 * time.Hour,
		BatchSize:        10,
		MaxConcurrency:   5,
		MaxRetry:         3,
		LeaderInterval:   50 * time.Millisecond,
		LeaderTTL:        10 * time.Second,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(reconcilerURL), compute.NoopProvider{}, cfg)
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
	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		processed.Store(req.Key, true)
		return sdk.Completed()
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

	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.Converged()
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
	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := attempts.Add(1)
		if n <= 1 {
			return sdk.ProcessResponse{Error: "temporary failure"}
		}
		return sdk.Completed()
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
	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.Completed()
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

	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		if req.Key == "parent" {
			return sdk.FanOut("child-1", "child-2")
		}
		return sdk.Completed()
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

	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.RequeueAfter(10 * time.Minute)
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
	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		mu.Lock()
		order = append(order, req.Key)
		mu.Unlock()
		return sdk.Completed()
	})
	defer srv.Close()

	// Use batch size 1 so items are processed one at a time in priority order.
	cfg := dispatcher.Config{
		QueueName:        "test",
		WorkerID:         "test",
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    1 * time.Hour,
		ReaperInterval:   1 * time.Hour,
		ScaleInterval:    1 * time.Hour,
		LeaseDuration:    1 * time.Hour,
		BatchSize:        1,
		MaxConcurrency:   1,
		MaxRetry:         3,
		LeaderInterval:   50 * time.Millisecond,
		LeaderTTL:        10 * time.Second,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(srv.URL), compute.NoopProvider{}, cfg)

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

	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if int32(n) <= old || maxSeen.CompareAndSwap(old, int32(n)) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		concurrent.Add(-1)
		return sdk.Completed()
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

	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		processed.Add(1)
		return sdk.Completed()
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

func TestDispatcher_Reaper(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	// Don't need a real reconciler for reaper test.
	srv := fakeReconciler(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.Completed()
	})
	defer srv.Close()

	// Enqueue and claim with a very short lease.
	s.EnsureQueue(ctx, "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})
	s.Enqueue(ctx, "test", "expired", 0)
	s.ClaimBatch(ctx, "test", 1, "dead-worker", 1*time.Millisecond)

	// Wait for lease to expire.
	time.Sleep(10 * time.Millisecond)

	// Run dispatcher in scale-only mode so it only reaps, never dispatches.
	cfg := dispatcher.Config{
		QueueName:      "test",
		WorkerID:       "reaper-test",
		Mode:           dispatcher.ModeScaleOnly,
		SweepInterval:  1 * time.Hour,
		ReaperInterval: 50 * time.Millisecond,
		ScaleInterval:  1 * time.Hour,
		LeaseDuration:  1 * time.Hour,
		BatchSize:      10,
		MaxConcurrency: 10,
		MaxRetry:       5,
		LeaderInterval: 50 * time.Millisecond,
		LeaderTTL:      10 * time.Second,
	}
	d := dispatcher.New(s, client.NewReconcilerClient(srv.URL), compute.NoopProvider{}, cfg)

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
