package inmem

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func BenchmarkEnqueue(b *testing.B) {
	s := New()
	ctx := context.Background()
	s.EnsureQueue(ctx, "bench", store.QueueConfig{MaxConcurrency: 1000, MaxRetry: 3})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Enqueue(ctx, "bench", fmt.Sprintf("key-%d", i), i%10)
	}
}

func BenchmarkClaimBatch(b *testing.B) {
	s := New()
	ctx := context.Background()
	s.EnsureQueue(ctx, "bench", store.QueueConfig{MaxConcurrency: 100000, MaxRetry: 3})
	for i := 0; i < 10000; i++ {
		s.Enqueue(ctx, "bench", fmt.Sprintf("key-%d", i), 0)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.ClaimBatch(ctx, "bench", 10, "worker", time.Hour)
	}
}

func BenchmarkComplete(b *testing.B) {
	s := New()
	ctx := context.Background()
	s.EnsureQueue(ctx, "bench", store.QueueConfig{MaxConcurrency: 100000, MaxRetry: 3})
	// Pre-enqueue and claim items
	for i := 0; i < b.N; i++ {
		s.Enqueue(ctx, "bench", fmt.Sprintf("key-%d", i), 0)
	}
	s.ClaimBatch(ctx, "bench", b.N, "worker", time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Complete(ctx, "bench", fmt.Sprintf("key-%d", i))
	}
}

func BenchmarkEnqueueBatch(b *testing.B) {
	s := New()
	ctx := context.Background()
	s.EnsureQueue(ctx, "bench", store.QueueConfig{MaxConcurrency: 1000, MaxRetry: 3})
	items := make([]store.BatchEnqueueItem, 100)
	for i := range items {
		items[i] = store.BatchEnqueueItem{Key: fmt.Sprintf("key-%d", i), Priority: 0}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Change keys each iteration to avoid dedup
		for j := range items {
			items[j].Key = fmt.Sprintf("key-%d-%d", i, j)
		}
		s.EnqueueBatch(ctx, "bench", items)
	}
}
