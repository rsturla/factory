package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/dispatcher"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
	"github.com/hummingbird-org/factory-workqueue/internal/wqapi"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/client"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/resync"
)

type resyncPlatform struct {
	store      store.Interface
	receiver   *httptest.Server
	reconciler *httptest.Server
	dispatcher *dispatcher.Dispatcher
	wq         *client.WorkqueueClient
}

func newResyncPlatform(t *testing.T, queue string, reconcilerFn func(reconciler.ProcessRequest) reconciler.ProcessResponse, opts ...func(*dispatcher.Config)) *resyncPlatform {
	t.Helper()

	s := inmem.New()
	if err := s.EnsureQueue(context.Background(), queue, store.QueueConfig{
		MaxConcurrency: 50, MaxRetry: 3,
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	receiver := httptest.NewServer(mux)

	rec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req reconciler.ProcessRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := reconcilerFn(req)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))

	cfg := dispatcher.Config{
		QueueName:        queue,
		WorkerID:         "resync-dispatcher",
		Mode:             dispatcher.ModePush,
		DispatchInterval: 50 * time.Millisecond,
		SweepInterval:    100 * time.Millisecond,
		ReaperInterval:   1 * time.Hour,
		LeaseDuration:    1 * time.Hour,
		BatchSize:        50,
		MaxConcurrency:   50,
		MaxRetry:         3,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	d := dispatcher.New(s, client.NewReconcilerClient(rec.URL), cfg)

	t.Cleanup(func() {
		receiver.Close()
		rec.Close()
	})

	return &resyncPlatform{
		store:      s,
		receiver:   receiver,
		reconciler: rec,
		dispatcher: d,
		wq:         client.NewWorkqueueClient(receiver.URL),
	}
}

func (p *resyncPlatform) runFor(t *testing.T, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	p.dispatcher.Run(ctx)
}

func TestEndToEnd_ResyncSharder(t *testing.T) {
	const queue = "resync-test"

	var mu sync.Mutex
	processed := make(map[string]bool)

	p := newResyncPlatform(t, queue, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		mu.Lock()
		processed[req.Key] = true
		mu.Unlock()
		return reconciler.Completed()
	})

	// Use period=tick so all keys land in shard. tick=1s keeps test fast.
	tick := 1 * time.Second
	keys := make([]string, 50)
	for i := range keys {
		keys[i] = fmt.Sprintf("repo-%d", i)
	}

	sh, err := resync.New(queue, tick, p.wq,
		resync.WithPriority(func(key string) int {
			if key == "repo-0" {
				return 100
			}
			return 0
		}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := sh.Process(t.Context(), tick, keys)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if result.InShard != len(keys) {
		t.Errorf("InShard=%d, want %d", result.InShard, len(keys))
	}
	if result.Enqueued != len(keys) {
		t.Errorf("Enqueued=%d, want %d", result.Enqueued, len(keys))
	}

	// Verify items are pending with NotBefore set.
	counts, err := p.store.CountByStatus(context.Background(), queue)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[store.StatusPending] != int64(len(keys)) {
		t.Errorf("pending=%d, want %d", counts[store.StatusPending], len(keys))
	}

	items, err := p.store.List(context.Background(), store.ListFilter{Queue: queue, Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, item := range items {
		if item.NotBefore == nil {
			t.Errorf("key %q missing NotBefore", item.Key)
		}
	}

	// Verify priority flowed through.
	repo0, err := p.store.GetItem(context.Background(), queue, "repo-0")
	if err != nil {
		t.Fatalf("GetItem repo-0: %v", err)
	}
	if repo0.Priority != 100 {
		t.Errorf("repo-0 priority=%d, want 100", repo0.Priority)
	}

	// Run dispatcher. tick=1s, so all items eligible within 1s. Run 3s for margin.
	p.runFor(t, 3*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != len(keys) {
		var missing []string
		for _, k := range keys {
			if !processed[k] {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) > 10 {
			missing = missing[:10]
		}
		t.Errorf("reconciled %d/%d keys, missing (first 10): %v", len(processed), len(keys), missing)
	}
}

func TestEndToEnd_ResyncPartialShard(t *testing.T) {
	const queue = "resync-partial"

	var mu sync.Mutex
	processed := make(map[string]bool)

	p := newResyncPlatform(t, queue, func(req reconciler.ProcessRequest) reconciler.ProcessResponse {
		mu.Lock()
		processed[req.Key] = true
		mu.Unlock()
		return reconciler.Completed()
	})

	tick := 1 * time.Second
	period := 10 * time.Second
	keys := make([]string, 500)
	for i := range keys {
		keys[i] = fmt.Sprintf("k-%d", i)
	}

	sh, err := resync.New(queue, tick, p.wq)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := sh.Process(t.Context(), period, keys)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	// With period=10s and tick=1s, roughly 1/10 of keys should be in shard.
	expectedApprox := len(keys) / 10
	if result.InShard == len(keys) {
		t.Error("expected partial shard when period > tick, got all keys")
	}
	if result.InShard == 0 {
		t.Error("expected some keys in shard, got 0")
	}
	// Allow wide tolerance: 20%-200% of expected.
	if result.InShard < expectedApprox/5 || result.InShard > expectedApprox*2 {
		t.Errorf("InShard=%d, expected roughly %d (±wide margin)", result.InShard, expectedApprox)
	}

	// Run dispatcher and verify only shard keys were processed.
	p.runFor(t, 3*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != result.InShard {
		t.Errorf("processed %d keys, but InShard was %d", len(processed), result.InShard)
	}
}

func TestEndToEnd_ResyncReceiverDown(t *testing.T) {
	const queue = "resync-down"

	s := inmem.New()
	s.EnsureQueue(context.Background(), queue, store.QueueConfig{MaxConcurrency: 10, MaxRetry: 3})

	mux := http.NewServeMux()
	wqapi.NewHandler(s, noop.Authorizer{}).Register(mux)
	receiver := httptest.NewServer(mux)
	wq := client.NewWorkqueueClient(receiver.URL)
	receiver.Close()

	sh, err := resync.New(queue, 1*time.Second, wq)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = sh.Process(t.Context(), 1*time.Second, []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected error when receiver is down")
	}
}
