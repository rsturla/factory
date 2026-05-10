package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/conformance"
	"github.com/hummingbird-org/factory-workqueue/internal/store/postgres"
)

func TestPostgresConformance(t *testing.T) {
	databaseURL := os.Getenv("PG_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://factory:factory@localhost:5432/factory?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Skipf("PostgreSQL not reachable: %v", err)
	}

	s := postgres.New(pool)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	conformance.Run(t, func(t *testing.T) store.Interface {
		// Clean all data between tests.
		pool.Exec(ctx, "TRUNCATE work_item_history, claim_queue, work_items, worker_leases, queue_state")

		if err := s.EnsureQueue(ctx, "test", store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
			ComputeBackend: "kubernetes",
		}); err != nil {
			t.Fatalf("EnsureQueue: %v", err)
		}
		return s
	})
}
