package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/sqlite"
)

func TestMigration_AppliesAll(t *testing.T) {
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var count int
	err = s.DB().QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count == 0 {
		t.Error("expected at least one migration applied, got 0")
	}
}

func TestMigration_Idempotent(t *testing.T) {
	path := t.TempDir() + "/test.db"

	s1, err := sqlite.New(path)
	if err != nil {
		t.Fatalf("New (first): %v", err)
	}
	s1.EnsureQueue(context.Background(), "test", store.QueueConfig{
		MaxConcurrency: 10, MaxRetry: 5,
	})
	s1.Enqueue(context.Background(), "test", "key-1", 42)

	var first int
	s1.DB().QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&first)
	s1.Close()

	s2, err := sqlite.New(path)
	if err != nil {
		t.Fatalf("New (second): %v", err)
	}
	defer s2.Close()

	item, err := s2.GetItem(context.Background(), "test", "key-1")
	if err != nil {
		t.Fatalf("GetItem after reopen: %v", err)
	}
	if item.Priority != 42 {
		t.Errorf("expected priority 42, got %d", item.Priority)
	}

	var second int
	s2.DB().QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&second)
	if second != first {
		t.Errorf("migration count changed on reopen: %d → %d", first, second)
	}
}

func TestMigration_TracksVersionsInOrder(t *testing.T) {
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rows, err := s.DB().Query("SELECT version FROM schema_migrations ORDER BY version")
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
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, table := range []string{
		"work_items", "work_item_history", "worker_leases",
		"queue_state", "schema_migrations", "active_leases",
	} {
		var name string
		err := s.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %s not created", table)
		} else if err != nil {
			t.Errorf("check table %s: %v", table, err)
		}
	}
}

func TestMigration_IndexesCreated(t *testing.T) {
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, idx := range []string{
		"idx_work_items_claimable",
		"idx_work_items_queue_status",
		"idx_history_queue_key",
		"idx_work_items_completed_at",
		"idx_active_leases_expiry",
	} {
		var name string
		err := s.DB().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("index %s not created", idx)
		} else if err != nil {
			t.Errorf("check index %s: %v", idx, err)
		}
	}
}

func TestMigration_AppliedAtSet(t *testing.T) {
	s, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var appliedAt string
	err = s.DB().QueryRow(
		"SELECT applied_at FROM schema_migrations WHERE version = 1",
	).Scan(&appliedAt)
	if err != nil {
		t.Fatalf("query applied_at: %v", err)
	}
	if appliedAt == "" {
		t.Error("applied_at should not be empty")
	}
}
