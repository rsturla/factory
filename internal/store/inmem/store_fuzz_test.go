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
