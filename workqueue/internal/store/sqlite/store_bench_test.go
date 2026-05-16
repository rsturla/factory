package sqlite_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/sqlite"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func setupBench(b *testing.B) *sqlite.Store {
	b.Helper()
	dir := b.TempDir()
	s, err := sqlite.New(filepath.Join(dir, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := s.EnsureQueue(ctx, "bench", store.QueueConfig{MaxConcurrency: 100000, MaxRetry: 5}); err != nil {
		b.Fatal(err)
	}
	return s
}

func BenchmarkEnqueue(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("enq-%08d", i), i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
}

func BenchmarkEnqueueBatch(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		items := make([]store.BatchEnqueueItem, 100)
		for j := range items {
			items[j] = store.BatchEnqueueItem{Key: fmt.Sprintf("eb-%08d-%03d", i, j), Priority: j % 10}
		}
		if _, err := s.EnqueueBatch(ctx, "bench", items); err != nil {
			b.Fatalf("EnqueueBatch: %v", err)
		}
	}
}

func BenchmarkClaimBatch(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	const batchSize = 10
	for i := range b.N * batchSize {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("cb-%08d", i), i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}

	b.ResetTimer()
	for range b.N {
		items, err := s.ClaimBatch(ctx, "bench", batchSize, "w1", 5*time.Minute)
		if err != nil {
			b.Fatalf("ClaimBatch: %v", err)
		}
		for _, item := range items {
			s.Complete(ctx, "bench", item.Key)
		}
	}
}

func BenchmarkComplete(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	for i := range b.N {
		s.Enqueue(ctx, "bench", fmt.Sprintf("cmp-%08d", i), 0)
	}
	s.ClaimBatch(ctx, "bench", b.N, "w1", time.Hour)

	b.ResetTimer()
	for i := range b.N {
		s.Complete(ctx, "bench", fmt.Sprintf("cmp-%08d", i))
	}
}

func BenchmarkTransition(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	for i := range b.N {
		s.Enqueue(ctx, "bench", fmt.Sprintf("tr-%08d", i), 0)
	}
	s.ClaimBatch(ctx, "bench", b.N, "w1", time.Hour)

	b.ResetTimer()
	for i := range b.N {
		s.Transition(ctx, "bench", fmt.Sprintf("tr-%08d", i), store.StatusClaimed, store.StatusRunning)
	}
}

func BenchmarkItemLifecycle(b *testing.B) {
	s := setupBench(b)
	defer s.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		key := fmt.Sprintf("bench-%08d", i)
		if err := s.Enqueue(ctx, "bench", key, i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
		items, err := s.ClaimBatch(ctx, "bench", 1, "w1", 5*time.Minute)
		if err != nil || len(items) == 0 {
			b.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
		}
		if err := s.Transition(ctx, "bench", key, store.StatusClaimed, store.StatusRunning); err != nil {
			b.Fatalf("Transition: %v", err)
		}
		if err := s.Complete(ctx, "bench", key); err != nil {
			b.Fatalf("Complete: %v", err)
		}
	}
}
