// Package integration tests the entire factory workqueue platform end-to-end.
//
// Each test spins up the full stack in-process: store, receiver (HTTP),
// dispatcher, and a fake reconciler. No external dependencies — uses the
// in-memory store.
//
// These tests verify that the components work together correctly, not just
// in isolation. They are suitable for running on Testing Farm or any CI
// environment.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cedar "github.com/hummingbird-org/factory-workqueue/internal/authz/cedar"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/compute"
	"github.com/hummingbird-org/factory-workqueue/internal/dispatcher"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

// platform holds all the components for an integration test.
type platform struct {
	store      store.Interface
	receiver   *httptest.Server
	reconciler *httptest.Server
	dispatcher *dispatcher.Dispatcher
	dispatchCfg dispatcher.Config
}

func newPlatform(t *testing.T, reconcilerFn func(sdk.ProcessRequest) sdk.ProcessResponse, opts ...func(*dispatcher.Config)) *platform {
	t.Helper()

	s := inmem.New()

	// Receiver with workqueue API.
	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	// Also add a simple /enqueue endpoint for convenience.
	mux.HandleFunc("POST /enqueue", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Key      string `json:"key"`
			Priority int    `json:"priority"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.Enqueue(r.Context(), "test", req.Key, req.Priority)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"enqueued"}`)
	})
	receiver := httptest.NewServer(mux)

	// Fake reconciler.
	reconciler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sdk.ProcessRequest
		json.NewDecoder(r.Body).Decode(&req)
		resp := reconcilerFn(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))

	cfg := dispatcher.Config{
		QueueName:        "test",
		WorkerID:         "test-dispatcher",
		Mode:             dispatcher.ModePush,
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    100 * time.Millisecond,
		ReaperInterval:   100 * time.Millisecond,
		ScaleInterval:    1 * time.Hour,
		LeaseDuration:    1 * time.Hour,
		BatchSize:        10,
		MaxConcurrency:   10,
		MaxRetry:         3,
	}

	for _, opt := range opts {
		opt(&cfg)
	}

	d := dispatcher.New(s, client.NewReconcilerClient(reconciler.URL), compute.NoopProvider{}, cfg)

	t.Cleanup(func() {
		receiver.Close()
		reconciler.Close()
	})

	return &platform{
		store:       s,
		receiver:    receiver,
		reconciler:  reconciler,
		dispatcher:  d,
		dispatchCfg: cfg,
	}
}

func (p *platform) enqueue(t *testing.T, key string, priority int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"key": key, "priority": priority})
	resp, err := http.Post(p.receiver.URL+"/enqueue", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("enqueue %s: %v", key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("enqueue %s: status %d", key, resp.StatusCode)
	}
}

func (p *platform) runFor(t *testing.T, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	p.dispatcher.Run(ctx)
}

// --- Tests ---

func TestEndToEnd_SingleItem(t *testing.T) {
	var processed atomic.Bool
	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		processed.Store(true)
		return sdk.Completed()
	})

	p.enqueue(t, "item-1", 0)
	p.runFor(t, 500*time.Millisecond)

	if !processed.Load() {
		t.Fatal("item was not processed")
	}

	counts, _ := p.store.CountByStatus(context.Background(), "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %v", counts)
	}
}

func TestEndToEnd_BatchProcessing(t *testing.T) {
	var count atomic.Int32
	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		count.Add(1)
		return sdk.Completed()
	})

	for i := range 20 {
		p.enqueue(t, fmt.Sprintf("batch-%d", i), 0)
	}

	p.runFor(t, 1*time.Second)

	if count.Load() != 20 {
		t.Errorf("expected 20 processed, got %d", count.Load())
	}
}

func TestEndToEnd_PriorityOrder(t *testing.T) {
	var order []string
	var mu sync.Mutex

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		mu.Lock()
		order = append(order, req.Key)
		mu.Unlock()
		return sdk.Completed()
	}, func(cfg *dispatcher.Config) {
		cfg.BatchSize = 1
		cfg.MaxConcurrency = 1
	})

	p.enqueue(t, "low", -10)
	p.enqueue(t, "critical", 100)
	p.enqueue(t, "normal", 0)

	p.runFor(t, 1*time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(order) < 3 {
		t.Fatalf("expected 3 processed, got %d: %v", len(order), order)
	}
	if order[0] != "critical" {
		t.Errorf("expected critical first, got %s", order[0])
	}
	if order[1] != "normal" {
		t.Errorf("expected normal second, got %s", order[1])
	}
	if order[2] != "low" {
		t.Errorf("expected low third, got %s", order[2])
	}
}

func TestEndToEnd_RetryOnFailure(t *testing.T) {
	var attempts atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := attempts.Add(1)
		if n <= 1 {
			return sdk.ProcessResponse{Error: "temporary failure"}
		}
		return sdk.Completed()
	})

	p.enqueue(t, "flaky", 0)

	// First attempt fails and requeues with backoff (not_before in the future).
	p.runFor(t, 500*time.Millisecond)

	if attempts.Load() < 1 {
		t.Fatalf("expected at least 1 attempt, got %d", attempts.Load())
	}

	// Verify item is back to pending (requeued after failure).
	item, _ := p.store.GetItem(context.Background(), "test", "flaky")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after retry, got %s", item.Status)
	}
}

func TestEndToEnd_DeadLetterAfterMaxRetry(t *testing.T) {
	// Manually drive the item through max retries by claiming and failing
	// directly, since the backoff delay makes it impractical to wait for
	// the dispatcher to retry within a test.
	ctx := context.Background()
	s := inmem.New()
	s.EnsureQueue(ctx, "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 3, ComputeBackend: "kubernetes",
	})

	s.Enqueue(ctx, "test", "doomed", 0)

	// Simulate 3 failed attempts.
	for i := range 3 {
		items, _ := s.ClaimBatch(ctx, "test", 1, "worker", time.Hour)
		if len(items) == 0 {
			t.Fatalf("attempt %d: nothing to claim", i+1)
		}
		s.Fail(ctx, "test", "doomed", "always fails")

		if i < 2 {
			// Requeue for next attempt.
			s.Requeue(ctx, "test", "doomed")
		} else {
			// Final attempt — dead-letter.
			s.Deadletter(ctx, "test", "doomed")
		}
	}

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusDeadLetter] != 1 {
		t.Errorf("expected 1 dead-lettered, got %v", counts)
	}
}

func TestEndToEnd_Converged(t *testing.T) {
	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.Converged()
	})

	p.enqueue(t, "already-done", 0)
	p.runFor(t, 500*time.Millisecond)

	counts, _ := p.store.CountByStatus(context.Background(), "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded (converged), got %v", counts)
	}
}

func TestEndToEnd_FanOut(t *testing.T) {
	var processed sync.Map

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		processed.Store(req.Key, true)
		if req.Key == "parent" {
			return sdk.FanOut("child-a", "child-b", "child-c")
		}
		return sdk.Completed()
	})

	p.enqueue(t, "parent", 0)
	p.runFor(t, 1*time.Second)

	for _, key := range []string{"parent", "child-a", "child-b", "child-c"} {
		if _, ok := processed.Load(key); !ok {
			t.Errorf("expected %s to be processed", key)
		}
	}
}

func TestEndToEnd_RequeueAfter(t *testing.T) {
	var invocations atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := invocations.Add(1)
		if n == 1 {
			return sdk.RequeueAfter(100 * time.Millisecond)
		}
		return sdk.Completed()
	})

	p.enqueue(t, "delayed", 0)
	p.runFor(t, 1*time.Second)

	if invocations.Load() < 2 {
		t.Errorf("expected at least 2 invocations (initial + after requeue), got %d", invocations.Load())
	}

	counts, _ := p.store.CountByStatus(context.Background(), "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %v", counts)
	}
}

func TestEndToEnd_ReconcilerUnreachable(t *testing.T) {
	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		return sdk.Completed()
	})

	// Close the reconciler to simulate unreachable.
	p.reconciler.Close()

	p.enqueue(t, "infra-fail", 0)
	p.runFor(t, 500*time.Millisecond)

	// Item should be back to pending (infra failure doesn't consume retry budget).
	item, _ := p.store.GetItem(context.Background(), "test", "infra-fail")
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 after infra failure, got %d", item.Attempts)
	}
}

func TestEndToEnd_ConcurrencyLimit(t *testing.T) {
	var concurrent atomic.Int32
	var maxSeen atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := concurrent.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		concurrent.Add(-1)
		return sdk.Completed()
	}, func(cfg *dispatcher.Config) {
		cfg.MaxConcurrency = 3
	})

	for i := range 10 {
		p.enqueue(t, fmt.Sprintf("conc-%d", i), 0)
	}

	p.runFor(t, 2*time.Second)

	if maxSeen.Load() > 3 {
		t.Errorf("expected max concurrent <= 3, saw %d", maxSeen.Load())
	}
}

func TestEndToEnd_Deduplication(t *testing.T) {
	var count atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		count.Add(1)
		return sdk.Completed()
	})

	// Enqueue the same key 5 times.
	for range 5 {
		p.enqueue(t, "dedup-key", 0)
	}

	p.runFor(t, 500*time.Millisecond)

	if count.Load() != 1 {
		t.Errorf("expected 1 processing (deduped), got %d", count.Load())
	}
}

func TestEndToEnd_ReEnqueueAfterComplete(t *testing.T) {
	var count atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		count.Add(1)
		return sdk.Completed()
	})

	// First round.
	p.enqueue(t, "reuse-key", 0)
	p.runFor(t, 500*time.Millisecond)

	// Re-enqueue same key after completion.
	p.enqueue(t, "reuse-key", 0)
	p.runFor(t, 500*time.Millisecond)

	if count.Load() != 2 {
		t.Errorf("expected 2 processings (enqueue after complete), got %d", count.Load())
	}
}

func TestEndToEnd_StandaloneWorkerViaAPI(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "ec2",
	})

	// Start receiver with workqueue API.
	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Use the HTTP workqueue client (what a standalone worker would use).
	wq := client.NewWorkqueueClient(srv.URL)

	// Enqueue via API.
	err := wq.Enqueue(context.Background(), "test", "standalone-item", 50)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// Claim via API.
	items, err := wq.ClaimBatch(context.Background(), "test", 1, "ec2-worker-1", 2*time.Hour)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 claimed, got %d", len(items))
	}
	if items[0].Key != "standalone-item" {
		t.Errorf("expected standalone-item, got %s", items[0].Key)
	}

	// Transition via API.
	err = wq.Transition(context.Background(), "test", "standalone-item", store.StatusClaimed, store.StatusRunning)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Heartbeat via API.
	err = wq.ExtendLease(context.Background(), "test", "standalone-item", 2*time.Hour)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Complete via API.
	err = wq.Complete(context.Background(), "test", "standalone-item")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Verify final state.
	counts, _ := wq.CountByStatus(context.Background(), "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %v", counts)
	}
}

func TestEndToEnd_StandaloneWorkerFailAndRetry(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "ec2",
	})

	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wq := client.NewWorkqueueClient(srv.URL)

	// Enqueue, claim, fail.
	wq.Enqueue(context.Background(), "test", "fail-item", 0)
	items, _ := wq.ClaimBatch(context.Background(), "test", 1, "worker", time.Hour)
	wq.Fail(context.Background(), "test", items[0].Key, "build failed")

	// Item should be failed.
	item, _ := wq.GetItem(context.Background(), "test", "fail-item")
	if item.Status != store.StatusFailed {
		t.Errorf("expected failed, got %s", item.Status)
	}

	// Requeue (what completion handler would do).
	wq.Requeue(context.Background(), "test", "fail-item")

	// Should be claimable again.
	items, _ = wq.ClaimBatch(context.Background(), "test", 1, "worker", time.Hour)
	if len(items) != 1 {
		t.Fatalf("expected 1 claimable after requeue, got %d", len(items))
	}
}

func TestEndToEnd_MultipleQueuesIsolated(t *testing.T) {
	var buildCount, testCount atomic.Int32

	s := inmem.New()
	s.EnsureQueue(context.Background(), "build", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 3, ComputeBackend: "kubernetes",
	})
	s.EnsureQueue(context.Background(), "test-run", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 3, ComputeBackend: "kubernetes",
	})

	// Two reconcilers — one per queue.
	buildReconciler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buildCount.Add(1)
		json.NewEncoder(w).Encode(sdk.Completed())
	}))
	defer buildReconciler.Close()

	testReconciler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testCount.Add(1)
		json.NewEncoder(w).Encode(sdk.Completed())
	}))
	defer testReconciler.Close()

	buildCfg := dispatcher.Config{
		QueueName: "build", WorkerID: "build-dispatcher", Mode: dispatcher.ModePush,
		DispatchInterval: 50 * time.Millisecond, SweepInterval: 1 * time.Hour,
		ReaperInterval: 1 * time.Hour, ScaleInterval: 1 * time.Hour,
		LeaseDuration: 1 * time.Hour, BatchSize: 10, MaxConcurrency: 10, MaxRetry: 3,
	}
	testCfg := buildCfg
	testCfg.QueueName = "test-run"
	testCfg.WorkerID = "test-dispatcher"

	buildDispatcher := dispatcher.New(s, client.NewReconcilerClient(buildReconciler.URL), compute.NoopProvider{}, buildCfg)
	testDispatcher := dispatcher.New(s, client.NewReconcilerClient(testReconciler.URL), compute.NoopProvider{}, testCfg)

	// Enqueue to both queues.
	s.Enqueue(context.Background(), "build", "pkg-1", 0)
	s.Enqueue(context.Background(), "build", "pkg-2", 0)
	s.Enqueue(context.Background(), "test-run", "test-1", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); buildDispatcher.Run(ctx) }()
	go func() { defer wg.Done(); testDispatcher.Run(ctx) }()
	wg.Wait()

	if buildCount.Load() != 2 {
		t.Errorf("expected 2 build items processed, got %d", buildCount.Load())
	}
	if testCount.Load() != 1 {
		t.Errorf("expected 1 test item processed, got %d", testCount.Load())
	}
}

func TestEndToEnd_ReaperReclaimsExpiredLease(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})

	// Claim with a very short lease.
	s.Enqueue(context.Background(), "test", "expired-item", 0)
	s.ClaimBatch(context.Background(), "test", 1, "dead-worker", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// Start a dispatcher with fast reaper.
	reconciled := make(chan string, 1)
	reconciler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sdk.ProcessRequest
		json.NewDecoder(r.Body).Decode(&req)
		reconciled <- req.Key
		json.NewEncoder(w).Encode(sdk.Completed())
	}))
	defer reconciler.Close()

	cfg := dispatcher.Config{
		QueueName: "test", WorkerID: "reaper-test", Mode: dispatcher.ModePush,
		DispatchInterval: 50 * time.Millisecond, SweepInterval: 100 * time.Millisecond,
		ReaperInterval: 50 * time.Millisecond, ScaleInterval: 1 * time.Hour,
		LeaseDuration: 1 * time.Hour, BatchSize: 10, MaxConcurrency: 10, MaxRetry: 5,
	}

	d := dispatcher.New(s, client.NewReconcilerClient(reconciler.URL), compute.NoopProvider{}, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	go d.Run(ctx)

	select {
	case key := <-reconciled:
		if key != "expired-item" {
			t.Errorf("expected expired-item, got %s", key)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not reclaim expired item")
	}
}

func TestEndToEnd_AuthzDeniesUnauthorized(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})

	// Use Cedar with a policy that only allows sre-team.
	policy := `
permit(
    principal,
    action,
    resource
) when {
    context.groups.contains("sre-team")
};`

	cedarauthz, err := cedar.NewFromBytes("test.cedar", []byte(policy))
	if err != nil {
		t.Fatalf("NewFromBytes: %v", err)
	}

	mux := http.NewServeMux()
	wqapi.NewHandler(s, cedarauthz).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Authorized request (sre-team).
	body, _ := json.Marshal(map[string]any{"queue": "test", "key": "allowed", "priority": 0})
	req, _ := http.NewRequest("POST", srv.URL+"/wq/enqueue", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", "sre-team")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("sre-team should be allowed, got %d", resp.StatusCode)
	}

	// Unauthorized request (random user).
	body, _ = json.Marshal(map[string]any{"queue": "test", "key": "denied", "priority": 0})
	req, _ = http.NewRequest("POST", srv.URL+"/wq/enqueue", bytes.NewReader(body))
	req.Header.Set("X-Forwarded-User", "eve")
	req.Header.Set("X-Forwarded-Groups", "random-team")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("random-team should be denied, got %d", resp.StatusCode)
	}

	// Unauthenticated request.
	body, _ = json.Marshal(map[string]any{"queue": "test", "key": "noauth", "priority": 0})
	req, _ = http.NewRequest("POST", srv.URL+"/wq/enqueue", bytes.NewReader(body))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("unauthenticated should be denied, got %d", resp.StatusCode)
	}
}

func TestEndToEnd_StandaloneWorkerHeartbeat(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "ec2",
	})

	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wq := client.NewWorkqueueClient(srv.URL)

	// Enqueue, claim with short lease.
	wq.Enqueue(context.Background(), "test", "heartbeat-item", 0)
	items, _ := wq.ClaimBatch(context.Background(), "test", 1, "worker", 1*time.Second)
	if len(items) != 1 {
		t.Fatalf("expected 1 claimed, got %d", len(items))
	}

	// Extend the lease.
	err := wq.ExtendLease(context.Background(), "test", "heartbeat-item", 2*time.Hour)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	// Verify lease was extended.
	item, _ := wq.GetItem(context.Background(), "test", "heartbeat-item")
	if item.LeaseExpires == nil {
		t.Fatal("expected lease_expires to be set")
	}
	until := time.Until(*item.LeaseExpires)
	if until < 1*time.Hour {
		t.Errorf("expected lease extended to ~2h, got %v remaining", until)
	}
}

func TestEndToEnd_AdminRetryAndCancel(t *testing.T) {
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5, ComputeBackend: "kubernetes",
	})

	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wq := client.NewWorkqueueClient(srv.URL)
	ctx := context.Background()

	// Enqueue, claim, fail.
	wq.Enqueue(ctx, "test", "retry-me", 0)
	wq.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	wq.Fail(ctx, "test", "retry-me", "broke")

	// Requeue (admin retry).
	wq.Requeue(ctx, "test", "retry-me")

	item, _ := wq.GetItem(ctx, "test", "retry-me")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending after retry, got %s", item.Status)
	}

	// Claim again and complete.
	wq.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	wq.Complete(ctx, "test", "retry-me")

	// Enqueue another, then cancel it.
	wq.Enqueue(ctx, "test", "cancel-me", 0)
	wq.Transition(ctx, "test", "cancel-me", store.StatusPending, store.StatusFailed)

	item, _ = wq.GetItem(ctx, "test", "cancel-me")
	if item.Status != store.StatusFailed {
		t.Errorf("expected failed after cancel, got %s", item.Status)
	}
}

func TestEndToEnd_ReconciliationPollingPattern(t *testing.T) {
	// Simulates the polling pattern: reconciler returns RequeueAfter
	// until external work completes, then returns Completed.
	var invocations atomic.Int32

	p := newPlatform(t, func(req sdk.ProcessRequest) sdk.ProcessResponse {
		n := invocations.Add(1)
		switch {
		case n == 1:
			// First call: "start the build"
			return sdk.RequeueAfter(50 * time.Millisecond)
		case n == 2:
			// Second call: "build still running"
			return sdk.RequeueAfter(50 * time.Millisecond)
		default:
			// Third call: "build done"
			return sdk.Completed()
		}
	})

	p.enqueue(t, "poll-item", 0)
	p.runFor(t, 1*time.Second)

	if invocations.Load() < 3 {
		t.Errorf("expected at least 3 invocations (poll pattern), got %d", invocations.Load())
	}

	counts, _ := p.store.CountByStatus(context.Background(), "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %v", counts)
	}
}
