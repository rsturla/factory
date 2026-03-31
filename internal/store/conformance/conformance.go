// Package conformance provides a shared test suite that all store.Interface
// implementations must pass.
package conformance

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

// Run executes the full conformance test suite against the given store implementation.
// The setup function should return a clean store with a queue named "test"
// configured with max_concurrency=10, max_retry=5.
func Run(t *testing.T, setup func(t *testing.T) store.Interface) {
	t.Run("Enqueue", func(t *testing.T) { testEnqueue(t, setup) })
	t.Run("EnqueueDedup", func(t *testing.T) { testEnqueueDedup(t, setup) })
	t.Run("ClaimBatch", func(t *testing.T) { testClaimBatch(t, setup) })
	t.Run("ClaimPriorityOrder", func(t *testing.T) { testClaimPriorityOrder(t, setup) })
	t.Run("ClaimConcurrencyLimit", func(t *testing.T) { testClaimConcurrencyLimit(t, setup) })
	t.Run("ClaimNotBefore", func(t *testing.T) { testClaimNotBefore(t, setup) })
	t.Run("Complete", func(t *testing.T) { testComplete(t, setup) })
	t.Run("Fail", func(t *testing.T) { testFail(t, setup) })
	t.Run("Requeue", func(t *testing.T) { testRequeue(t, setup) })
	t.Run("RequeueUndoAttempt", func(t *testing.T) { testRequeueUndoAttempt(t, setup) })
	t.Run("Deadletter", func(t *testing.T) { testDeadletter(t, setup) })
	t.Run("ExtendLease", func(t *testing.T) { testExtendLease(t, setup) })
	t.Run("Transition", func(t *testing.T) { testTransition(t, setup) })
	t.Run("CountByStatus", func(t *testing.T) { testCountByStatus(t, setup) })
	t.Run("List", func(t *testing.T) { testList(t, setup) })
	t.Run("RepairCounter", func(t *testing.T) { testRepairCounter(t, setup) })
	t.Run("GetItem", func(t *testing.T) { testGetItem(t, setup) })
	t.Run("History", func(t *testing.T) { testHistory(t, setup) })
	t.Run("ListQueues", func(t *testing.T) { testListQueues(t, setup) })
	t.Run("PurgeDeadLetters", func(t *testing.T) { testPurgeDeadLetters(t, setup) })
	t.Run("ConcurrentClaim", func(t *testing.T) { testConcurrentClaim(t, setup) })
	t.Run("EnqueueWhileInFlight", func(t *testing.T) { testEnqueueWhileInFlight(t, setup) })
	t.Run("ReEnqueueAfterComplete", func(t *testing.T) { testReEnqueueAfterComplete(t, setup) })
	t.Run("CompleteWrongStatus", func(t *testing.T) { testCompleteWrongStatus(t, setup) })
	t.Run("FailPreservesError", func(t *testing.T) { testFailPreservesError(t, setup) })
	t.Run("LeaseExpiry", func(t *testing.T) { testLeaseExpiry(t, setup) })
	t.Run("FullLifecycle", func(t *testing.T) { testFullLifecycle(t, setup) })
	t.Run("EnqueueLowerPriorityNoOp", func(t *testing.T) { testEnqueueLowerPriorityNoOp(t, setup) })
	t.Run("MultipleQueuesIsolated", func(t *testing.T) { testMultipleQueuesIsolated(t, setup) })
	t.Run("EnsureQueueUpdatesConfig", func(t *testing.T) { testEnsureQueueUpdatesConfig(t, setup) })
}

func testEnqueue(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	if err := s.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusPending] != 1 {
		t.Fatalf("expected 1 pending, got %d", counts[store.StatusPending])
	}
}

func testEnqueueDedup(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.Enqueue(ctx, "test", "key-1", 10)
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusPending] != 1 {
		t.Fatalf("expected 1 pending after dedup, got %d", counts[store.StatusPending])
	}
	items, _ := s.List(ctx, store.ListFilter{Queue: "test"})
	if len(items) != 1 || items[0].Priority != 10 {
		t.Fatalf("expected priority 10, got %v", items)
	}
}

func testClaimBatch(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	for i := range 5 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}
	items, err := s.ClaimBatch(ctx, "test", 3, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3, got %d", len(items))
	}
	for _, item := range items {
		if item.Status != store.StatusClaimed {
			t.Errorf("expected claimed, got %s", item.Status)
		}
		if item.Attempts != 1 {
			t.Errorf("expected attempts=1, got %d", item.Attempts)
		}
	}
}

func testClaimPriorityOrder(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "low", -10)
	s.Enqueue(ctx, "test", "high", 100)
	s.Enqueue(ctx, "test", "normal", 0)
	items, _ := s.ClaimBatch(ctx, "test", 3, "w", time.Hour)
	if items[0].Key != "high" || items[1].Key != "normal" || items[2].Key != "low" {
		t.Errorf("wrong order: %s, %s, %s", items[0].Key, items[1].Key, items[2].Key)
	}
}

func testClaimConcurrencyLimit(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	for i := range 15 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}
	items, _ := s.ClaimBatch(ctx, "test", 15, "w", time.Hour)
	if len(items) != 10 {
		t.Fatalf("expected 10 (max), got %d", len(items))
	}
	items2, _ := s.ClaimBatch(ctx, "test", 15, "w2", time.Hour)
	if len(items2) != 0 {
		t.Fatalf("expected 0 at capacity, got %d", len(items2))
	}
	s.Complete(ctx, "test", items[0].Key)
	items3, _ := s.ClaimBatch(ctx, "test", 15, "w2", time.Hour)
	if len(items3) != 1 {
		t.Fatalf("expected 1 after complete, got %d", len(items3))
	}
}

func testClaimNotBefore(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	future := time.Now().Add(time.Hour)
	s.Enqueue(ctx, "test", "future", 0, store.WithNotBefore(future))
	items, _ := s.ClaimBatch(ctx, "test", 10, "w", time.Hour)
	if len(items) != 0 {
		t.Fatalf("expected 0, got %d", len(items))
	}
}

func testComplete(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	if err := s.Complete(ctx, "test", "key-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Fatalf("expected 1 succeeded, got %d", counts[store.StatusSucceeded])
	}
}

func testFail(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Fail(ctx, "test", "key-1", "broke")
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusFailed] != 1 {
		t.Fatalf("expected 1 failed, got %d", counts[store.StatusFailed])
	}
}

func testRequeue(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Requeue(ctx, "test", "key-1")
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusPending] != 1 {
		t.Fatalf("expected 1 pending, got %d", counts[store.StatusPending])
	}
	items, _ := s.List(ctx, store.ListFilter{Queue: "test"})
	if items[0].Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", items[0].Attempts)
	}
}

func testRequeueUndoAttempt(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.RequeueUndoAttempt(ctx, "test", "key-1", time.Now().Add(30*time.Second))
	items, _ := s.List(ctx, store.ListFilter{Queue: "test"})
	if items[0].Attempts != 0 {
		t.Fatalf("expected attempts=0, got %d", items[0].Attempts)
	}
}

func testDeadletter(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Deadletter(ctx, "test", "key-1")
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusDeadLetter] != 1 {
		t.Fatalf("expected 1 dead_letter, got %d", counts[store.StatusDeadLetter])
	}
}

func testExtendLease(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Minute)
	if err := s.ExtendLease(ctx, "test", "key-1", 2*time.Hour); err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}
	err := s.ExtendLease(ctx, "test", "nonexistent", time.Hour)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testTransition(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	if err := s.Transition(ctx, "test", "key-1", store.StatusClaimed, store.StatusRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	err := s.Transition(ctx, "test", "key-1", store.StatusClaimed, store.StatusRunning)
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func testCountByStatus(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	for i := range 5 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}
	s.ClaimBatch(ctx, "test", 2, "w", time.Hour)
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusPending] != 3 || counts[store.StatusClaimed] != 2 {
		t.Errorf("expected 3 pending + 2 claimed, got %v", counts)
	}
}

func testList(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	for i := range 5 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), i)
	}
	items, _ := s.List(ctx, store.ListFilter{Queue: "test", Limit: 2})
	if len(items) != 2 {
		t.Fatalf("expected 2, got %d", len(items))
	}
	if items[0].Priority < items[1].Priority {
		t.Errorf("expected descending priority")
	}
}

func testRepairCounter(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	for i := range 3 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}
	s.ClaimBatch(ctx, "test", 3, "w", time.Hour)
	s.RepairCounter(ctx, "test")
	items, _ := s.ClaimBatch(ctx, "test", 10, "w2", time.Hour)
	if len(items) != 0 {
		t.Fatalf("expected 0, got %d", len(items))
	}
}

func testGetItem(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 42)
	item, err := s.GetItem(ctx, "test", "key-1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Key != "key-1" || item.Priority != 42 {
		t.Errorf("unexpected item: %+v", item)
	}
	_, err = s.GetItem(ctx, "test", "nonexistent")
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testHistory(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Complete(ctx, "test", "key-1")

	history, err := s.GetItemHistory(ctx, "test", "key-1")
	if err != nil {
		t.Fatalf("GetItemHistory: %v", err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 history entries, got %d", len(history))
	}
}

func testListQueues(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	queues, err := s.ListQueues(ctx)
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(queues) < 1 {
		t.Fatalf("expected at least 1 queue, got %d", len(queues))
	}
	found := false
	for _, q := range queues {
		if q.Name == "test" {
			found = true
			if q.Counts["pending"] != 1 {
				t.Errorf("expected 1 pending count, got %d", q.Counts["pending"])
			}
		}
	}
	if !found {
		t.Fatalf("queue 'test' not found in ListQueues")
	}
}

func testPurgeDeadLetters(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Deadletter(ctx, "test", "key-1")
	count, err := s.PurgeDeadLetters(ctx, "test")
	if err != nil {
		t.Fatalf("PurgeDeadLetters: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 purged, got %d", count)
	}
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusDeadLetter] != 0 {
		t.Fatalf("expected 0 dead_letter after purge, got %d", counts[store.StatusDeadLetter])
	}
}

// testConcurrentClaim verifies that N concurrent claimers never double-claim an item.
// Each item should be claimed by exactly one goroutine.
func testConcurrentClaim(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	// Enqueue 20 items.
	for i := range 20 {
		s.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0)
	}

	// 5 concurrent claimers, each trying to claim 10.
	var mu sync.Mutex
	allClaimed := make(map[string]string) // key → worker who claimed it

	var wg sync.WaitGroup
	for w := range 5 {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			items, err := s.ClaimBatch(ctx, "test", 10, workerID, time.Hour)
			if err != nil {
				t.Errorf("worker %s claim error: %v", workerID, err)
				return
			}
			mu.Lock()
			for _, item := range items {
				if prev, ok := allClaimed[item.Key]; ok {
					t.Errorf("key %s claimed by both %s and %s", item.Key, prev, workerID)
				}
				allClaimed[item.Key] = workerID
			}
			mu.Unlock()
		}(fmt.Sprintf("worker-%d", w))
	}
	wg.Wait()

	// Max concurrency is 10, so at most 10 items should be claimed total.
	if len(allClaimed) > 10 {
		t.Fatalf("expected at most 10 claimed (max_concurrency), got %d", len(allClaimed))
	}
	if len(allClaimed) == 0 {
		t.Fatal("expected at least 1 claimed item")
	}
}

// testEnqueueWhileInFlight verifies that enqueueing a key that is currently
// claimed/running does NOT overwrite the in-flight item.
func testEnqueueWhileInFlight(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	s.Enqueue(ctx, "test", "key-1", 0)
	items, _ := s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	if len(items) != 1 {
		t.Fatalf("expected 1 claimed, got %d", len(items))
	}

	// Enqueue the same key while it's claimed — should be a no-op.
	s.Enqueue(ctx, "test", "key-1", 100)

	// The item should still be claimed, not reverted to pending.
	item, err := s.GetItem(ctx, "test", "key-1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.Status != store.StatusClaimed {
		t.Errorf("expected claimed, got %s (enqueue overwrote in-flight item)", item.Status)
	}
}

// testReEnqueueAfterComplete verifies that a key can be re-enqueued
// after it has been completed.
func testReEnqueueAfterComplete(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	// First round: enqueue → claim → complete.
	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)
	s.Complete(ctx, "test", "key-1")

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Fatalf("expected 1 succeeded, got %d", counts[store.StatusSucceeded])
	}

	// Second round: re-enqueue the same key.
	err := s.Enqueue(ctx, "test", "key-1", 10)
	if err != nil {
		t.Fatalf("re-enqueue after complete: %v", err)
	}

	// Should be claimable again — enqueue resets completed items to pending.
	items, _ := s.ClaimBatch(ctx, "test", 1, "w2", time.Hour)
	if len(items) != 1 {
		t.Fatalf("expected 1 claimable after re-enqueue, got %d", len(items))
	}
	if items[0].Key != "key-1" {
		t.Errorf("expected key-1, got %s", items[0].Key)
	}
}

// testCompleteWrongStatus verifies that Complete/Fail on a pending item
// returns ErrNotFound (only claimed/running items can be completed).
func testCompleteWrongStatus(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	s.Enqueue(ctx, "test", "key-1", 0)

	// Complete on a pending item should fail.
	err := s.Complete(ctx, "test", "key-1")
	if err != store.ErrNotFound {
		t.Errorf("Complete on pending: expected ErrNotFound, got %v", err)
	}

	// Fail on a pending item should fail.
	err = s.Fail(ctx, "test", "key-1", "nope")
	if err != store.ErrNotFound {
		t.Errorf("Fail on pending: expected ErrNotFound, got %v", err)
	}

	// Complete on nonexistent item.
	err = s.Complete(ctx, "test", "nonexistent")
	if err != store.ErrNotFound {
		t.Errorf("Complete on nonexistent: expected ErrNotFound, got %v", err)
	}
}

// testFailPreservesError verifies that the error message from Fail
// is preserved and retrievable via GetItem.
func testFailPreservesError(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	s.Enqueue(ctx, "test", "key-1", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)

	errMsg := "connection refused: reconciler at http://localhost:8082"
	s.Fail(ctx, "test", "key-1", errMsg)

	item, err := s.GetItem(ctx, "test", "key-1")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.ErrorMessage != errMsg {
		t.Errorf("expected error message %q, got %q", errMsg, item.ErrorMessage)
	}
}

// testLeaseExpiry verifies that an item claimed with a very short lease
// has a LeaseExpires timestamp in the near future.
func testLeaseExpiry(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	s.Enqueue(ctx, "test", "key-1", 0)
	items, _ := s.ClaimBatch(ctx, "test", 1, "w", 5*time.Second)
	if len(items) != 1 {
		t.Fatalf("expected 1, got %d", len(items))
	}

	item, _ := s.GetItem(ctx, "test", "key-1")
	if item.LeaseExpires == nil {
		t.Fatal("expected LeaseExpires to be set")
	}

	// Lease should expire within the next 10 seconds.
	until := time.Until(*item.LeaseExpires)
	if until > 10*time.Second || until < -5*time.Second {
		t.Errorf("lease expiry seems wrong: expires in %v", until)
	}
}

// testFullLifecycle exercises the complete lifecycle:
// enqueue → claim → transition to running → complete, verifying
// history records each step.
func testFullLifecycle(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	// Enqueue.
	s.Enqueue(ctx, "test", "lifecycle-key", 50)

	// Claim.
	items, _ := s.ClaimBatch(ctx, "test", 1, "worker-lifecycle", time.Hour)
	if len(items) != 1 || items[0].Key != "lifecycle-key" {
		t.Fatalf("claim: expected lifecycle-key, got %v", items)
	}
	if items[0].Priority != 50 {
		t.Errorf("expected priority 50, got %d", items[0].Priority)
	}

	// Transition claimed → running.
	err := s.Transition(ctx, "test", "lifecycle-key",
		store.StatusClaimed, store.StatusRunning,
		store.WithWorkerID("worker-lifecycle"))
	if err != nil {
		t.Fatalf("transition: %v", err)
	}

	// Verify status is running.
	item, _ := s.GetItem(ctx, "test", "lifecycle-key")
	if item.Status != store.StatusRunning {
		t.Errorf("expected running, got %s", item.Status)
	}

	// Complete.
	err = s.Complete(ctx, "test", "lifecycle-key")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Verify history has at least 3 entries: pending→claimed, claimed→running, running→succeeded.
	history, _ := s.GetItemHistory(ctx, "test", "lifecycle-key")
	if len(history) < 3 {
		t.Errorf("expected at least 3 history entries, got %d", len(history))
	}

	// Verify final counts.
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %d", counts[store.StatusSucceeded])
	}
	if counts[store.StatusPending] != 0 {
		t.Errorf("expected 0 pending, got %d", counts[store.StatusPending])
	}
}

// testEnqueueLowerPriorityNoOp verifies that enqueueing a key with lower
// priority than the existing pending item does not reduce its priority.
func testEnqueueLowerPriorityNoOp(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	s.Enqueue(ctx, "test", "key-1", 100)
	s.Enqueue(ctx, "test", "key-1", 10) // lower priority

	items, _ := s.List(ctx, store.ListFilter{Queue: "test"})
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Priority != 100 {
		t.Errorf("expected priority 100 (should not decrease), got %d", items[0].Priority)
	}
}

// testMultipleQueuesIsolated verifies that operations on one queue
// don't affect another queue.
func testMultipleQueuesIsolated(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	// Create a second queue.
	s.EnsureQueue(ctx, "other", store.QueueConfig{
		MaxConcurrency: 5,
		MaxRetry:       3,
		ComputeBackend: "ec2",
	})

	// Enqueue to both queues.
	s.Enqueue(ctx, "test", "shared-key", 0)
	s.Enqueue(ctx, "other", "shared-key", 0)

	// Claim from "test" only.
	items, _ := s.ClaimBatch(ctx, "test", 10, "w", time.Hour)
	if len(items) != 1 {
		t.Fatalf("expected 1 from test, got %d", len(items))
	}

	// "other" should still have 1 pending.
	counts, _ := s.CountByStatus(ctx, "other")
	if counts[store.StatusPending] != 1 {
		t.Errorf("expected 1 pending in other, got %d", counts[store.StatusPending])
	}

	// Complete in "test" shouldn't affect "other".
	s.Complete(ctx, "test", "shared-key")

	counts, _ = s.CountByStatus(ctx, "other")
	if counts[store.StatusPending] != 1 {
		t.Errorf("other queue affected by test complete: got %d pending", counts[store.StatusPending])
	}
}

// testEnsureQueueUpdatesConfig verifies that calling EnsureQueue on an
// existing queue updates its configuration without losing state.
func testEnsureQueueUpdatesConfig(t *testing.T, setup func(t *testing.T) store.Interface) {
	ctx := context.Background()
	s := setup(t)

	// Queue "test" was created by setup with max_concurrency=10.
	// Enqueue and claim an item to create in-progress state.
	s.Enqueue(ctx, "test", "config-test", 0)
	s.ClaimBatch(ctx, "test", 1, "w", time.Hour)

	// Update the config.
	s.EnsureQueue(ctx, "test", store.QueueConfig{
		MaxConcurrency: 50,
		MaxRetry:       10,
		ComputeBackend: "ec2",
	})

	// Verify config was updated via ListQueues.
	queues, err := s.ListQueues(ctx)
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	var found *store.QueueInfo
	for i := range queues {
		if queues[i].Name == "test" {
			found = &queues[i]
			break
		}
	}
	if found == nil {
		t.Fatal("queue 'test' not found")
	}
	if found.MaxConcurrency != 50 {
		t.Errorf("expected max_concurrency=50 after update, got %d", found.MaxConcurrency)
	}
	if found.MaxRetry != 10 {
		t.Errorf("expected max_retry=10 after update, got %d", found.MaxRetry)
	}
	if found.ComputeBackend != "ec2" {
		t.Errorf("expected compute_backend=ec2 after update, got %s", found.ComputeBackend)
	}

	// Verify in-progress item was not lost.
	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusClaimed] != 1 {
		t.Errorf("expected 1 claimed item preserved after config update, got %v", counts)
	}
}
