package postgres_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/postgres"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func setupBench(tb testing.TB) (*pgxpool.Pool, *postgres.Store) {
	tb.Helper()
	databaseURL := os.Getenv("PG_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://factory:factory@localhost:5432/factory?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		tb.Skipf("PostgreSQL not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		tb.Skipf("PostgreSQL not reachable: %v", err)
	}

	s := postgres.New(pool)
	if err := s.Migrate(ctx); err != nil {
		pool.Close()
		tb.Fatalf("Migrate: %v", err)
	}

	pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state")
	if err := s.EnsureQueue(ctx, "bench", store.QueueConfig{
		MaxConcurrency: 10000,
		MaxRetry:       5,
		ComputeBackend: "noop",
	}); err != nil {
		pool.Close()
		tb.Fatalf("EnsureQueue: %v", err)
	}

	return pool, s
}

func TestHOTUpdateRatio(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	pool.Exec(ctx, "VACUUM FULL work_items")
	pool.Exec(ctx, "SELECT pg_stat_reset()")
	time.Sleep(100 * time.Millisecond)

	const n = 500
	for i := range n {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("hot-%04d", i), i); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}

	pool.Exec(ctx, "VACUUM FULL work_items")
	pool.Exec(ctx, "SELECT pg_stat_reset()")
	time.Sleep(100 * time.Millisecond)

	items, err := s.ClaimBatch(ctx, "bench", n, "worker-1", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}

	for _, item := range items {
		if err := s.Transition(ctx, "bench", item.Key, store.StatusClaimed, store.StatusRunning); err != nil {
			t.Fatalf("Transition: %v", err)
		}
	}
	for _, item := range items {
		if err := s.Complete(ctx, "bench", item.Key); err != nil {
			t.Fatalf("Complete: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	var updates, hotUpdates int64
	err = pool.QueryRow(ctx, `
		SELECT n_tup_upd, n_tup_hot_upd
		FROM pg_stat_user_tables WHERE relname = 'work_items'
	`).Scan(&updates, &hotUpdates)
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}

	if updates == 0 {
		t.Fatal("no updates recorded in pg_stat_user_tables")
	}

	hotPct := float64(hotUpdates) / float64(updates) * 100
	t.Logf("work_items: %d updates, %d HOT (%.1f%%)", updates, hotUpdates, hotPct)

	// With the active_leases side-table, no index on work_items references
	// the status column. All updates are HOT-eligible. The limiting factor
	// is page space: fillfactor=70 leaves 30% free per page, and each item
	// gets 3 updates (claim→transition→complete). This yields ~72-77% HOT
	// locally. CI with -race and parallel tests sees ~58-65% due to stat
	// counter noise. A status-referencing index would drop HOT to ~0%.
	if hotPct < 55 {
		t.Errorf("HOT ratio %.1f%% is below 55%% threshold — check for status-referencing indexes on work_items", hotPct)
	}
}

func TestClaimQueueConsistency(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	countClaimQueue := func(queue string) int {
		var n int
		pool.QueryRow(ctx, "SELECT count(*) FROM claim_queue WHERE queue = $1", queue).Scan(&n)
		return n
	}

	if err := s.Enqueue(ctx, "bench", "cq-1", 5); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := countClaimQueue("bench"); got != 1 {
		t.Errorf("after enqueue: claim_queue count = %d, want 1", got)
	}

	items, err := s.ClaimBatch(ctx, "bench", 1, "w1", 5*time.Minute)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimBatch: err=%v items=%d", err, len(items))
	}
	if got := countClaimQueue("bench"); got != 0 {
		t.Errorf("after claim: claim_queue count = %d, want 0", got)
	}

	if err := s.Requeue(ctx, "bench", "cq-1"); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if got := countClaimQueue("bench"); got != 1 {
		t.Errorf("after requeue: claim_queue count = %d, want 1", got)
	}

	items, _ = s.ClaimBatch(ctx, "bench", 1, "w1", 5*time.Minute)
	if err := s.Complete(ctx, "bench", "cq-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := countClaimQueue("bench"); got != 0 {
		t.Errorf("after complete: claim_queue count = %d, want 0", got)
	}

	if err := s.Enqueue(ctx, "bench", "cq-1", 5); err != nil {
		t.Fatalf("re-Enqueue: %v", err)
	}
	if got := countClaimQueue("bench"); got != 1 {
		t.Errorf("after re-enqueue: claim_queue count = %d, want 1", got)
	}
}

func TestTransitionClaimQueue(t *testing.T) {
	pool, s := setupBench(t)
	defer pool.Close()
	ctx := context.Background()

	countClaimQueue := func(queue string) int {
		var n int
		pool.QueryRow(ctx, "SELECT count(*) FROM claim_queue WHERE queue = $1", queue).Scan(&n)
		return n
	}

	if err := s.Enqueue(ctx, "bench", "tr-1", 5); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	items, _ := s.ClaimBatch(ctx, "bench", 1, "w1", 5*time.Minute)
	if len(items) != 1 {
		t.Fatal("expected 1 claimed item")
	}
	if err := s.Deadletter(ctx, "bench", "tr-1"); err != nil {
		t.Fatalf("Deadletter: %v", err)
	}

	if err := s.Transition(ctx, "bench", "tr-1", store.StatusDeadLetter, store.StatusPending); err != nil {
		t.Fatalf("Transition dead_letter→pending: %v", err)
	}
	if got := countClaimQueue("bench"); got != 1 {
		t.Errorf("after retry transition: claim_queue count = %d, want 1", got)
	}

	if err := s.Transition(ctx, "bench", "tr-1", store.StatusPending, store.StatusFailed); err != nil {
		t.Fatalf("Transition pending→failed: %v", err)
	}
	if got := countClaimQueue("bench"); got != 0 {
		t.Errorf("after cancel transition: claim_queue count = %d, want 0", got)
	}
}

func BenchmarkEnqueue(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
	ctx := context.Background()

	b.ResetTimer()
	for i := range b.N {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("enq-%08d", i), i%10); err != nil {
			b.Fatalf("Enqueue: %v", err)
		}
	}
}

func BenchmarkEnqueueBatch(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
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

func BenchmarkComplete(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
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
	pool, s := setupBench(b)
	defer pool.Close()
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

func BenchmarkListExpiredLeases(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
	ctx := context.Background()

	for i := range 500 {
		s.Enqueue(ctx, "bench", fmt.Sprintf("exp-%04d", i), 0)
	}
	s.ClaimBatch(ctx, "bench", 500, "w1", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	b.ResetTimer()
	for range b.N {
		s.ListExpiredLeases(ctx, "bench", 100)
	}
}

func BenchmarkConcurrentClaim(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
	ctx := context.Background()

	for i := range b.N * 10 {
		s.Enqueue(ctx, "bench", fmt.Sprintf("cc-%08d", i), i%10)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			items, _ := s.ClaimBatch(ctx, "bench", 10, "w", 5*time.Minute)
			for _, item := range items {
				s.Complete(ctx, "bench", item.Key)
			}
		}
	})
}

func BenchmarkItemLifecycle(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
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

func BenchmarkClaimBatch(b *testing.B) {
	pool, s := setupBench(b)
	defer pool.Close()
	ctx := context.Background()

	const batchSize = 50
	for i := range b.N * batchSize {
		if err := s.Enqueue(ctx, "bench", fmt.Sprintf("cb-%08d", i), i%100); err != nil {
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
