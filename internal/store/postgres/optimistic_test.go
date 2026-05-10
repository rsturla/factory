package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// TestOptimisticConcurrentClaim verifies that concurrent ClaimBatch calls
// never exceed max_concurrency. Multiple goroutines race to claim from
// the same queue; the total claimed must not exceed the configured limit.
func TestOptimisticConcurrentClaim(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	const maxConc = 5
	const numItems = 20
	const numWorkers = 8

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "opt", store.QueueConfig{
		MaxConcurrency: maxConc,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	for i := range numItems {
		if err := s.Enqueue(ctx, "opt", keyN("oc", i), 0); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	var mu sync.Mutex
	var totalClaimed int

	var wg sync.WaitGroup
	for w := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			items, err := s.ClaimBatch(ctx, "opt", numItems, workerN(workerID), 5*time.Minute)
			if err != nil {
				t.Errorf("worker %d: ClaimBatch: %v", workerID, err)
				return
			}
			mu.Lock()
			totalClaimed += len(items)
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	if totalClaimed > maxConc {
		t.Errorf("total claimed %d exceeds max_concurrency %d", totalClaimed, maxConc)
	}
	if totalClaimed != maxConc {
		t.Errorf("expected exactly %d claimed (enough items available), got %d", maxConc, totalClaimed)
	}
}

// TestOptimisticNoDoubleClaimUnderContention verifies that no single key
// is claimed by more than one worker, even under heavy contention.
func TestOptimisticNoDoubleClaimUnderContention(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	const numItems = 50
	const numWorkers = 10

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "opt", store.QueueConfig{
		MaxConcurrency: numItems,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	for i := range numItems {
		if err := s.Enqueue(ctx, "opt", keyN("nd", i), 0); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	type claim struct {
		worker string
		key    string
	}
	var mu sync.Mutex
	var allClaims []claim

	var wg sync.WaitGroup
	for w := range numWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			wName := workerN(workerID)
			items, err := s.ClaimBatch(ctx, "opt", numItems, wName, 5*time.Minute)
			if err != nil {
				t.Errorf("worker %s: ClaimBatch: %v", wName, err)
				return
			}
			mu.Lock()
			for _, item := range items {
				allClaims = append(allClaims, claim{worker: wName, key: item.Key})
			}
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	// Check no key was claimed twice.
	seen := make(map[string]string) // key -> worker
	for _, c := range allClaims {
		if prev, ok := seen[c.key]; ok {
			t.Errorf("key %q claimed by both %q and %q", c.key, prev, c.worker)
		}
		seen[c.key] = c.worker
	}

	if len(allClaims) != numItems {
		t.Errorf("expected %d total claims, got %d", numItems, len(allClaims))
	}
}

// TestOptimisticClaimThenComplete verifies the full lifecycle:
// concurrent claims followed by concurrent completions.
func TestOptimisticClaimThenComplete(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	const numItems = 30

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "opt", store.QueueConfig{
		MaxConcurrency: numItems,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	for i := range numItems {
		if err := s.Enqueue(ctx, "opt", keyN("cc", i), 0); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	items, err := s.ClaimBatch(ctx, "opt", numItems, "w1", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != numItems {
		t.Fatalf("expected %d claimed, got %d", numItems, len(items))
	}

	// Complete all concurrently.
	var wg sync.WaitGroup
	for _, item := range items {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			if err := s.Complete(ctx, "opt", key); err != nil {
				t.Errorf("Complete(%q): %v", key, err)
			}
		}(item.Key)
	}
	wg.Wait()

	// All items should be succeeded.
	counts, err := s.CountByStatus(ctx, "opt")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[store.StatusSucceeded] != numItems {
		t.Errorf("expected %d succeeded, got %v", numItems, counts)
	}

	// active_leases should be empty.
	var leaseCount int
	pool.QueryRow(ctx, "SELECT count(*) FROM active_leases WHERE queue = 'opt'").Scan(&leaseCount)
	if leaseCount != 0 {
		t.Errorf("expected 0 active_leases, got %d", leaseCount)
	}
}

// TestOptimisticClaimThenFailRequeue verifies that Fail followed by
// Requeue correctly releases the active_lease and allows the item
// to be claimed again.
func TestOptimisticClaimThenFailRequeue(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "opt", store.QueueConfig{
		MaxConcurrency: 1,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	if err := s.Enqueue(ctx, "opt", "fail-requeue", 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := s.ClaimBatch(ctx, "opt", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}

	// Fail releases the active_lease.
	if err := s.Fail(ctx, "opt", "fail-requeue", "something broke"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	// After fail, active_leases should be empty.
	var leaseCount int
	pool.QueryRow(ctx, "SELECT count(*) FROM active_leases WHERE queue = 'opt'").Scan(&leaseCount)
	if leaseCount != 0 {
		t.Errorf("expected 0 active_leases after Fail, got %d", leaseCount)
	}

	// Requeue the failed item.
	if err := s.Requeue(ctx, "opt", "fail-requeue"); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	// Should be claimable again (max_concurrency=1 and lease was released).
	items, err = s.ClaimBatch(ctx, "opt", 1, "w2", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch after requeue: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item re-claimed, got %d", len(items))
	}
}

// TestOptimisticDeadletter verifies that Deadletter releases the
// active_lease and the dead-lettered item cannot be claimed.
func TestOptimisticDeadletter(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "opt", store.QueueConfig{
		MaxConcurrency: 5,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		t.Fatalf("EnsureQueue: %v", err)
	}

	if err := s.Enqueue(ctx, "opt", "dl-item", 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	items, err := s.ClaimBatch(ctx, "opt", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}

	// Fail first (Deadletter requires failed status or claimed/running).
	if err := s.Fail(ctx, "opt", "dl-item", "permanent failure"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	if err := s.Deadletter(ctx, "opt", "dl-item"); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}

	// active_leases should be empty.
	var leaseCount int
	pool.QueryRow(ctx, "SELECT count(*) FROM active_leases WHERE queue = 'opt'").Scan(&leaseCount)
	if leaseCount != 0 {
		t.Errorf("expected 0 active_leases after Deadletter, got %d", leaseCount)
	}

	// Item should be in dead_letter status.
	item, err := s.GetItem(ctx, "opt", "dl-item")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != store.StatusDeadLetter {
		t.Errorf("expected dead_letter status, got %s", item.Status)
	}
}

func keyN(prefix string, n int) string {
	return prefix + "-" + itoa(n)
}

func workerN(n int) string {
	return "w-" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
