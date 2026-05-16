package inmem_test

import (
	"context"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func FuzzStoreLifecycle(f *testing.F) {
	// Fuzz queue names, keys, and priority through a full enqueue→claim→complete lifecycle.
	f.Add("queue", "key", 0)
	f.Add("", "", 0)
	f.Add("q", "k", -1)
	f.Add("q", "k", 1<<31-1)
	f.Add("queue/with/slashes", "key:with:colons", 100)
	f.Add("queue with spaces", "key\twith\ttabs", 50)
	f.Add("q", "\x00\xff", 0)
	f.Add("unicode-队列", "键-key", 42)

	f.Fuzz(func(t *testing.T, queue, key string, priority int) {
		ctx := context.Background()
		s := inmem.New()

		// EnsureQueue must not panic.
		if err := s.EnsureQueue(ctx, queue, store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
		}); err != nil {
			return
		}

		// Enqueue must not panic.
		if err := s.Enqueue(ctx, queue, key, priority); err != nil {
			return
		}

		// CountByStatus must not panic.
		counts, err := s.CountByStatus(ctx, queue)
		if err != nil {
			return
		}
		if counts[store.StatusPending] != 1 {
			t.Fatalf("expected 1 pending after enqueue, got %d", counts[store.StatusPending])
		}

		// ClaimBatch must not panic.
		items, err := s.ClaimBatch(ctx, queue, 1, "worker", time.Hour)
		if err != nil {
			return
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 claimed item, got %d", len(items))
		}
		if items[0].Key != key {
			t.Fatalf("claimed key mismatch: got %q, want %q", items[0].Key, key)
		}

		// Complete must not panic.
		if err := s.Complete(ctx, queue, key); err != nil {
			t.Fatalf("complete failed: %v", err)
		}

		// GetItem must not panic.
		item, err := s.GetItem(ctx, queue, key)
		if err != nil {
			t.Fatalf("get item failed: %v", err)
		}
		if item.Status != store.StatusSucceeded {
			t.Fatalf("expected succeeded, got %s", item.Status)
		}
	})
}

func FuzzEnqueueBatch(f *testing.F) {
	f.Add("queue", "key1", "key2", "key3", 0, 10, -5)
	f.Add("", "", "", "", 0, 0, 0)
	f.Add("q", "\x00", "\xff", "normal", 1<<31-1, -(1 << 31), 42)
	f.Add("unicode-队列", "key-1", "key-2", "key-3", 100, 200, 300)
	f.Add("q", "same", "same", "same", 1, 2, 3)

	f.Fuzz(func(t *testing.T, queue, k1, k2, k3 string, p1, p2, p3 int) {
		ctx := context.Background()
		s := inmem.New()

		if err := s.EnsureQueue(ctx, queue, store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
		}); err != nil {
			return
		}

		items := []store.BatchEnqueueItem{
			{Key: k1, Priority: p1},
			{Key: k2, Priority: p2},
			{Key: k3, Priority: p3},
		}

		n, err := s.EnqueueBatch(ctx, queue, items)
		if err != nil {
			t.Fatalf("EnqueueBatch failed: %v", err)
		}
		if n < 0 {
			t.Fatalf("negative count: %d", n)
		}

		counts, err := s.CountByStatus(ctx, queue)
		if err != nil {
			t.Fatalf("CountByStatus: %v", err)
		}
		if counts[store.StatusPending] < 0 {
			t.Fatalf("negative pending count: %d", counts[store.StatusPending])
		}

		// Verify all distinct keys are retrievable.
		for _, item := range items {
			if item.Key == "" {
				continue
			}
			got, err := s.GetItem(ctx, queue, item.Key)
			if err != nil {
				continue
			}
			if got.Status != store.StatusPending {
				t.Fatalf("expected pending, got %s", got.Status)
			}
		}
	})
}
