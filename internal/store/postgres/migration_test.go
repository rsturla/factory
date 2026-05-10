package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/internal/store/postgres"
)

func connectForMigrationTest(t *testing.T) (*pgxpool.Pool, *postgres.Store) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://factory:factory@localhost:5432/factory?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("PostgreSQL not reachable: %v", err)
	}

	// Clean slate.
	pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, work_item_history, claim_queue, work_items, worker_leases, queue_state CASCADE")

	s := postgres.New(pool)
	return pool, s
}

func TestMigration_AppliesAll(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var count int
	err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 2 migrations applied, got %d", count)
	}
}

func TestMigration_Idempotent(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	// Run twice.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (first): %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second): %v", err)
	}

	var count int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if count != 5 {
		t.Errorf("expected 2 migrations after double run, got %d", count)
	}
}

func TestMigration_TracksVersionsInOrder(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := pool.Query(ctx, "SELECT version, filename FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	expected := []struct {
		version  int
		filename string
	}{
		{1, "001_initial.sql"},
		{2, "002_add_completed_index.sql"},
		{3, "003_add_queue_paused.sql"},
		{4, "004_queue_performance_tuning.sql"},
		{5, "005_claim_queue.sql"},
	}

	i := 0
	for rows.Next() {
		var version int
		var filename string
		rows.Scan(&version, &filename)
		if i >= len(expected) {
			t.Fatalf("unexpected extra migration: version=%d", version)
		}
		if version != expected[i].version || filename != expected[i].filename {
			t.Errorf("migration %d: got version=%d filename=%s, want version=%d filename=%s",
				i, version, filename, expected[i].version, expected[i].filename)
		}
		i++
	}
	if i != len(expected) {
		t.Errorf("expected %d migrations, got %d", len(expected), i)
	}
}

func TestMigration_TablesCreated(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, table := range []string{
		"work_items", "work_item_history", "worker_leases",
		"queue_state", "schema_migrations", "claim_queue",
	} {
		var exists bool
		pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists)
		if !exists {
			t.Errorf("table %s not created", table)
		}
	}
}

func TestMigration_IndexesCreated(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, idx := range []string{
		"idx_claim_queue_dispatch",
		"idx_history_queue_key",
	} {
		var exists bool
		pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)", idx,
		).Scan(&exists)
		if !exists {
			t.Errorf("index %s not created", idx)
		}
	}
}

func TestMigration_AppliedAtSet(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var appliedAt string
	err := pool.QueryRow(ctx,
		"SELECT applied_at::text FROM schema_migrations WHERE version = 1",
	).Scan(&appliedAt)
	if err != nil {
		t.Fatalf("query applied_at: %v", err)
	}
	if appliedAt == "" {
		t.Error("applied_at should not be empty")
	}
}
