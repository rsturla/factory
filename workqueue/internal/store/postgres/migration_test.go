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
	databaseURL := os.Getenv("PG_DATABASE_URL")
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
	pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations, work_item_history, claim_queue, active_leases, work_items, worker_leases, queue_state CASCADE")

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
	if count == 0 {
		t.Error("expected at least one migration applied, got 0")
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

	var first int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&first)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second): %v", err)
	}

	var second int
	pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&second)
	if second != first {
		t.Errorf("migration count changed on second run: %d → %d", first, second)
	}
}

func TestMigration_TracksVersionsInOrder(t *testing.T) {
	pool, s := connectForMigrationTest(t)
	defer pool.Close()
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	prev := 0
	count := 0
	for rows.Next() {
		var version int
		rows.Scan(&version)
		if version <= prev {
			t.Errorf("migration versions not strictly increasing: %d after %d", version, prev)
		}
		prev = version
		count++
	}
	if count == 0 {
		t.Error("no migrations found")
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
		"active_leases",
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
		"idx_active_leases_expiry",
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
