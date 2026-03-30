// Package conformance provides a shared test suite that both the PostgreSQL
// and in-memory workqueue implementations must pass.
package conformance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

// Run executes the full conformance test suite against the given workqueue implementation.
// The setup function should return a clean implementation with a queue named "test"
// configured with max_concurrency=10, max_retry=5.
func Run(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
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
}

func testEnqueue(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	err := wq.Enqueue(ctx, "test", "key-1", 0)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[workqueue.StatusPending] != 1 {
		t.Fatalf("expected 1 pending, got %d", counts[workqueue.StatusPending])
	}
}

func testEnqueueDedup(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	// Enqueue same key twice — second call should merge priority upward.
	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	if err := wq.Enqueue(ctx, "test", "key-1", 10); err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[workqueue.StatusPending] != 1 {
		t.Fatalf("expected 1 pending after dedup, got %d", counts[workqueue.StatusPending])
	}

	// Verify priority was merged upward.
	items, err := wq.List(ctx, workqueue.ListFilter{Queue: "test"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Priority != 10 {
		t.Fatalf("expected priority 10, got %v", items)
	}
}

func testClaimBatch(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	for i := range 5 {
		if err := wq.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	items, err := wq.ClaimBatch(ctx, "test", 3, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 claimed, got %d", len(items))
	}

	for _, item := range items {
		if item.Status != workqueue.StatusClaimed {
			t.Errorf("expected status claimed, got %s", item.Status)
		}
		if item.WorkerID != "worker-1" {
			t.Errorf("expected worker-1, got %s", item.WorkerID)
		}
		if item.Attempts != 1 {
			t.Errorf("expected attempts=1, got %d", item.Attempts)
		}
	}

	// Remaining items should still be pending.
	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[workqueue.StatusPending] != 2 {
		t.Fatalf("expected 2 pending, got %d", counts[workqueue.StatusPending])
	}
}

func testClaimPriorityOrder(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	// Enqueue items with different priorities.
	if err := wq.Enqueue(ctx, "test", "low", -10); err != nil {
		t.Fatal(err)
	}
	if err := wq.Enqueue(ctx, "test", "high", 100); err != nil {
		t.Fatal(err)
	}
	if err := wq.Enqueue(ctx, "test", "normal", 0); err != nil {
		t.Fatal(err)
	}

	items, err := wq.ClaimBatch(ctx, "test", 3, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3, got %d", len(items))
	}
	if items[0].Key != "high" {
		t.Errorf("expected first item to be 'high', got %s", items[0].Key)
	}
	if items[1].Key != "normal" {
		t.Errorf("expected second item to be 'normal', got %s", items[1].Key)
	}
	if items[2].Key != "low" {
		t.Errorf("expected third item to be 'low', got %s", items[2].Key)
	}
}

func testClaimConcurrencyLimit(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	// Queue configured with max_concurrency=10, enqueue 15 items.
	for i := range 15 {
		if err := wq.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}

	// First claim: should get 10 (max_concurrency).
	items, err := wq.ClaimBatch(ctx, "test", 15, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 10 {
		t.Fatalf("expected 10 (max_concurrency), got %d", len(items))
	}

	// Second claim: should get 0 (at capacity).
	items2, err := wq.ClaimBatch(ctx, "test", 15, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch 2: %v", err)
	}
	if len(items2) != 0 {
		t.Fatalf("expected 0 at capacity, got %d", len(items2))
	}

	// Complete one item, then claim again — should get 1.
	if err := wq.Complete(ctx, "test", items[0].Key); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	items3, err := wq.ClaimBatch(ctx, "test", 15, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch 3: %v", err)
	}
	if len(items3) != 1 {
		t.Fatalf("expected 1 after completing one, got %d", len(items3))
	}
}

func testClaimNotBefore(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	future := time.Now().Add(time.Hour)
	if err := wq.Enqueue(ctx, "test", "future-key", 0, workqueue.WithNotBefore(future)); err != nil {
		t.Fatal(err)
	}

	// Should not be claimable yet.
	items, err := wq.ClaimBatch(ctx, "test", 10, "worker-1", time.Hour)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 (not_before in future), got %d", len(items))
	}
}

func testComplete(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	items, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: %v, items=%d", err, len(items))
	}

	if err := wq.Complete(ctx, "test", "key-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if counts[workqueue.StatusSucceeded] != 1 {
		t.Fatalf("expected 1 succeeded, got %d", counts[workqueue.StatusSucceeded])
	}
}

func testFail(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	items, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: %v, items=%d", err, len(items))
	}

	if err := wq.Fail(ctx, "test", "key-1", "something broke"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if counts[workqueue.StatusFailed] != 1 {
		t.Fatalf("expected 1 failed, got %d", counts[workqueue.StatusFailed])
	}
}

func testRequeue(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	items, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: %v", err)
	}

	if err := wq.Requeue(ctx, "test", "key-1"); err != nil {
		t.Fatalf("Requeue: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if counts[workqueue.StatusPending] != 1 {
		t.Fatalf("expected 1 pending after requeue, got %d", counts[workqueue.StatusPending])
	}

	// Attempt count should still be 1 (set at claim time, not undone by Requeue).
	listItems, err := wq.List(ctx, workqueue.ListFilter{Queue: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if listItems[0].Attempts != 1 {
		t.Fatalf("expected attempts=1 after requeue, got %d", listItems[0].Attempts)
	}
}

func testRequeueUndoAttempt(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	items, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: %v", err)
	}

	notBefore := time.Now().Add(30 * time.Second)
	if err := wq.RequeueUndoAttempt(ctx, "test", "key-1", notBefore); err != nil {
		t.Fatalf("RequeueUndoAttempt: %v", err)
	}

	// Attempt count should be back to 0.
	listItems, err := wq.List(ctx, workqueue.ListFilter{Queue: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if listItems[0].Attempts != 0 {
		t.Fatalf("expected attempts=0 after undo, got %d", listItems[0].Attempts)
	}
}

func testDeadletter(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	_, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if err := wq.Deadletter(ctx, "test", "key-1"); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if counts[workqueue.StatusDeadLetter] != 1 {
		t.Fatalf("expected 1 dead_letter, got %d", counts[workqueue.StatusDeadLetter])
	}
}

func testExtendLease(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	items, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatal(err)
	}

	if err := wq.ExtendLease(ctx, "test", "key-1", 2*time.Hour); err != nil {
		t.Fatalf("ExtendLease: %v", err)
	}

	// Should not error on unclaimed item.
	err = wq.ExtendLease(ctx, "test", "nonexistent", time.Hour)
	if err != workqueue.ErrNotFound {
		t.Fatalf("expected ErrNotFound for nonexistent, got %v", err)
	}
}

func testTransition(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	if err := wq.Enqueue(ctx, "test", "key-1", 0); err != nil {
		t.Fatal(err)
	}
	_, err := wq.ClaimBatch(ctx, "test", 1, "worker-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	// Valid transition: claimed → running.
	if err := wq.Transition(ctx, "test", "key-1", workqueue.StatusClaimed, workqueue.StatusRunning); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Invalid transition: should get ErrConflict.
	err = wq.Transition(ctx, "test", "key-1", workqueue.StatusClaimed, workqueue.StatusRunning)
	if err != workqueue.ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Not found.
	err = wq.Transition(ctx, "test", "nonexistent", workqueue.StatusPending, workqueue.StatusClaimed)
	if err != workqueue.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testCountByStatus(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	for i := range 5 {
		if err := wq.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = wq.ClaimBatch(ctx, "test", 2, "worker-1", time.Hour)

	counts, err := wq.CountByStatus(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if counts[workqueue.StatusPending] != 3 {
		t.Errorf("expected 3 pending, got %d", counts[workqueue.StatusPending])
	}
	if counts[workqueue.StatusClaimed] != 2 {
		t.Errorf("expected 2 claimed, got %d", counts[workqueue.StatusClaimed])
	}
}

func testList(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	for i := range 5 {
		if err := wq.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), i); err != nil {
			t.Fatal(err)
		}
	}

	// List with limit.
	items, err := wq.List(ctx, workqueue.ListFilter{Queue: "test", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	// Should be ordered by priority DESC.
	if items[0].Priority < items[1].Priority {
		t.Errorf("expected descending priority order")
	}

	// List with status filter.
	pending := workqueue.StatusPending
	items, err = wq.List(ctx, workqueue.ListFilter{Queue: "test", Status: &pending})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5 pending items, got %d", len(items))
	}
}

func testRepairCounter(t *testing.T, setup func(t *testing.T) workqueue.Interface) {
	ctx := context.Background()
	wq := setup(t)

	for i := range 3 {
		if err := wq.Enqueue(ctx, "test", fmt.Sprintf("key-%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = wq.ClaimBatch(ctx, "test", 3, "worker-1", time.Hour)

	// Repair should reconcile the counter.
	if err := wq.RepairCounter(ctx, "test"); err != nil {
		t.Fatalf("RepairCounter: %v", err)
	}

	// Should still be able to claim nothing (3 in progress, max 10, but 0 pending).
	items, err := wq.ClaimBatch(ctx, "test", 10, "worker-2", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 remaining, got %d", len(items))
	}
}
