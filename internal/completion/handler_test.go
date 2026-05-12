package completion_test

import (
	"context"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/completion"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func setup(t *testing.T) (store.Interface, *completion.Handler) {
	t.Helper()
	s := inmem.New()
	s.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10,
		MaxRetry:       5,
	})
	h := completion.NewHandler(s, completion.Config{
		MaxAttempts:    3,
		BackoffBase:    100 * time.Millisecond,
		BackoffMax:     1 * time.Second,
		JitterFraction: 0.0, // no jitter for deterministic tests
	})
	return s, h
}

func enqueueAndClaim(t *testing.T, s store.Interface, key string) store.WorkItem {
	t.Helper()
	ctx := context.Background()
	s.Enqueue(ctx, "test", key, 0)
	items, err := s.ClaimBatch(ctx, "test", 1, "worker", time.Hour)
	if err != nil || len(items) != 1 {
		t.Fatalf("claim failed: err=%v, items=%d", err, len(items))
	}
	return items[0]
}

func TestHandleSuccess(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)
	enqueueAndClaim(t, s, "key-1")

	if err := h.HandleSuccess(ctx, "test", "key-1"); err != nil {
		t.Fatalf("HandleSuccess: %v", err)
	}

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusSucceeded] != 1 {
		t.Errorf("expected 1 succeeded, got %d", counts[store.StatusSucceeded])
	}
}

func TestHandleFailure_Requeues(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)
	enqueueAndClaim(t, s, "key-1")

	// Attempt 1 of 3 — should requeue, not deadletter.
	if err := h.HandleFailure(ctx, "test", "key-1", 1, "temporary error"); err != nil {
		t.Fatalf("HandleFailure: %v", err)
	}

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusPending] != 1 {
		t.Errorf("expected 1 pending (requeued), got counts=%v", counts)
	}

	// Verify not_before is set (backoff delay).
	item, _ := s.GetItem(ctx, "test", "key-1")
	if item.NotBefore == nil {
		t.Error("expected NotBefore to be set after requeue with backoff")
	}
}

func TestHandleFailure_Deadletters(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)
	enqueueAndClaim(t, s, "key-1")

	// Attempt 3 (== MaxAttempts) should deadletter, not requeue.
	if err := h.HandleFailure(ctx, "test", "key-1", 3, "final error"); err != nil {
		t.Fatalf("HandleFailure: %v", err)
	}

	counts, _ := s.CountByStatus(ctx, "test")
	if counts[store.StatusDeadLetter] != 1 {
		t.Errorf("expected 1 dead_letter after max attempts, got counts=%v", counts)
	}
}

func TestHandleFailure_BackoffIncreases(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)

	// Test two separate items to avoid needing to reclaim through backoff.
	enqueueAndClaim(t, s, "key-1")
	before1 := time.Now()
	h.HandleFailure(ctx, "test", "key-1", 1, "error")
	item1, _ := s.GetItem(ctx, "test", "key-1")

	enqueueAndClaim(t, s, "key-2")
	before2 := time.Now()
	h.HandleFailure(ctx, "test", "key-2", 2, "error")
	item2, _ := s.GetItem(ctx, "test", "key-2")

	if item1.NotBefore == nil || item2.NotBefore == nil {
		t.Fatal("expected NotBefore to be set on both items")
	}

	delay1 := item1.NotBefore.Sub(before1)
	delay2 := item2.NotBefore.Sub(before2)

	if delay2 <= delay1 {
		t.Errorf("expected increasing backoff: attempt1=%v, attempt2=%v", delay1, delay2)
	}
}

func TestHandleInfraFailure_DoesNotConsumeRetryBudget(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)
	enqueueAndClaim(t, s, "key-1")

	if err := h.HandleInfraFailure(ctx, "test", "key-1"); err != nil {
		t.Fatalf("HandleInfraFailure: %v", err)
	}

	item, _ := s.GetItem(ctx, "test", "key-1")
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 after infra failure (undo), got %d", item.Attempts)
	}
	if item.Status != store.StatusPending {
		t.Errorf("expected pending, got %s", item.Status)
	}
}

func TestHandleRequeueAfter(t *testing.T) {
	ctx := context.Background()
	s, h := setup(t)
	enqueueAndClaim(t, s, "key-1")

	delay := 5 * time.Minute
	if err := h.HandleRequeueAfter(ctx, "test", "key-1", delay); err != nil {
		t.Fatalf("HandleRequeueAfter: %v", err)
	}

	item, _ := s.GetItem(ctx, "test", "key-1")
	if item.Status != store.StatusPending {
		t.Errorf("expected pending, got %s", item.Status)
	}
	if item.Attempts != 0 {
		t.Errorf("expected attempts=0 (undo), got %d", item.Attempts)
	}
	if item.NotBefore == nil {
		t.Error("expected NotBefore to be set")
	} else {
		until := time.Until(*item.NotBefore)
		if until < 4*time.Minute || until > 6*time.Minute {
			t.Errorf("expected ~5m delay, got %v", until)
		}
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := completion.DefaultConfig()
	if cfg.MaxAttempts != 5 {
		t.Errorf("expected MaxAttempts=5, got %d", cfg.MaxAttempts)
	}
	if cfg.BackoffBase != 30*time.Second {
		t.Errorf("expected BackoffBase=30s, got %v", cfg.BackoffBase)
	}
	if cfg.BackoffMax != 10*time.Minute {
		t.Errorf("expected BackoffMax=10m, got %v", cfg.BackoffMax)
	}
	if cfg.JitterFraction != 0.25 {
		t.Errorf("expected JitterFraction=0.25, got %v", cfg.JitterFraction)
	}
}
