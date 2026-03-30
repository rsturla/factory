// Package conformance provides a shared test suite that all store.Interface
// implementations must pass.
package conformance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hummingbird-org/factory/internal/store"
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
