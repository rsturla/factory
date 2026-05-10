// Package postgres implements store.Interface backed by PostgreSQL.
//
// It uses SELECT FOR UPDATE SKIP LOCKED for zero-contention job claiming,
// upsert with GREATEST for priority-merging deduplication, and a meta row
// in queue_state for O(1) concurrency tracking.
package postgres

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store implements store.Interface using PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a new PostgreSQL store.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool returns the underlying connection pool (for use in tests or migrations).
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// Migrate runs versioned SQL migrations from the embedded migrations/ directory.
// Each migration runs exactly once. Uses an advisory lock to prevent concurrent
// migration races when multiple services start simultaneously.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Advisory lock prevents concurrent migrations from racing.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(42)"); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}

	// Create the migrations tracking table if it doesn't exist.
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			filename TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Read all migration files, sorted by name.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // 001_initial.sql, 002_add_index.sql, ...

	// Determine which migrations have already been applied.
	applied := make(map[int]bool)
	rows, err := tx.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("query applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration version: %w", err)
		}
		applied[v] = true
	}
	rows.Close()

	// Apply new migrations in order.
	for _, filename := range files {
		version, err := parseMigrationVersion(filename)
		if err != nil {
			return fmt.Errorf("parse migration filename %q: %w", filename, err)
		}

		if applied[version] {
			continue
		}

		sql, err := migrationsFS.ReadFile("migrations/" + filename)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filename, err)
		}

		slog.Info("applying migration", "version", version, "filename", filename)

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", filename, err)
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (version, filename) VALUES ($1, $2)
		`, version, filename); err != nil {
			return fmt.Errorf("record migration %s: %w", filename, err)
		}
	}

	return tx.Commit(ctx)
}

// parseMigrationVersion extracts the version number from a filename like "001_initial.sql".
func parseMigrationVersion(filename string) (int, error) {
	parts := strings.SplitN(filename, "_", 2)
	if len(parts) < 2 {
		return 0, fmt.Errorf("expected format NNN_name.sql, got %q", filename)
	}
	var version int
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("non-numeric version in %q", filename)
		}
		version = version*10 + int(c-'0')
	}
	return version, nil
}

// --- Work Queue Operations ---

func (s *Store) Enqueue(ctx context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	o := store.ApplyEnqueueOptions(opts)
	_, err := s.pool.Exec(ctx, `
		WITH upserted AS (
			INSERT INTO work_items (queue, key, priority, not_before)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (queue, key) DO UPDATE SET
				priority = CASE
					WHEN work_items.status = 'pending'
					THEN GREATEST(work_items.priority, EXCLUDED.priority)
					ELSE EXCLUDED.priority
				END,
				status = 'pending',
				attempts = CASE WHEN work_items.status = 'pending' THEN work_items.attempts ELSE 0 END,
				not_before = EXCLUDED.not_before,
				worker_id = NULL,
				lease_expires = NULL,
				error_message = NULL,
				claimed_at = NULL,
				completed_at = NULL,
				updated_at = now()
			WHERE work_items.status IN ('pending', 'succeeded', 'failed', 'dead_letter')
			RETURNING queue, key, priority, not_before, created_at
		),
		lease_cleanup AS (
			DELETE FROM active_leases WHERE queue = $1 AND key = $2
		)
		INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
		SELECT queue, key, priority, not_before, created_at FROM upserted
		ON CONFLICT (queue, key) DO UPDATE SET
			priority = GREATEST(claim_queue.priority, EXCLUDED.priority),
			not_before = EXCLUDED.not_before
	`, queue, key, priority, o.NotBefore)
	return err
}

func (s *Store) EnqueueBatch(ctx context.Context, queue string, items []store.BatchEnqueueItem) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	keys := make([]string, len(items))
	priorities := make([]int, len(items))
	notBefores := make([]*time.Time, len(items))
	for i, item := range items {
		keys[i] = item.Key
		priorities[i] = item.Priority
		notBefores[i] = item.NotBefore
	}

	tag, err := s.pool.Exec(ctx, `
		WITH upserted AS (
			INSERT INTO work_items (queue, key, priority, not_before)
			SELECT $1, unnest($2::text[]), unnest($3::int[]), unnest($4::timestamptz[])
			ON CONFLICT (queue, key) DO UPDATE SET
				priority = CASE
					WHEN work_items.status = 'pending'
					THEN GREATEST(work_items.priority, EXCLUDED.priority)
					ELSE EXCLUDED.priority
				END,
				status = 'pending',
				attempts = CASE WHEN work_items.status = 'pending' THEN work_items.attempts ELSE 0 END,
				not_before = EXCLUDED.not_before,
				worker_id = NULL,
				lease_expires = NULL,
				error_message = NULL,
				claimed_at = NULL,
				completed_at = NULL,
				updated_at = now()
			WHERE work_items.status IN ('pending', 'succeeded', 'failed', 'dead_letter')
			RETURNING queue, key, priority, not_before, created_at
		),
		lease_cleanup AS (
			DELETE FROM active_leases WHERE queue = $1 AND key = ANY($2::text[])
		)
		INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
		SELECT queue, key, priority, not_before, created_at FROM upserted
		ON CONFLICT (queue, key) DO UPDATE SET
			priority = GREATEST(claim_queue.priority, EXCLUDED.priority),
			not_before = EXCLUDED.not_before
	`, queue, keys, priorities, notBefores)
	if err != nil {
		return 0, fmt.Errorf("enqueue batch: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the queue_state row to serialize concurrent ClaimBatch calls
	// (~1-5ms hold time). Read max_concurrency from queue_state, then
	// derive current concurrency from count(active_leases) instead of
	// a driftable in_progress counter.
	var maxConc int
	err = tx.QueryRow(ctx, `
		SELECT max_concurrency FROM queue_state
		WHERE queue = $1 FOR UPDATE
	`, queue).Scan(&maxConc)
	if err != nil {
		return nil, fmt.Errorf("read queue_state: %w", err)
	}

	var activeCount int
	err = tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM active_leases WHERE queue = $1
	`, queue).Scan(&activeCount)
	if err != nil {
		return nil, fmt.Errorf("count active_leases: %w", err)
	}

	remaining := maxConc - activeCount
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	// Single CTE: claim items, insert leases, update counter, record history.
	rows, err := tx.Query(ctx, `
		WITH claimed_keys AS (
			DELETE FROM claim_queue
			WHERE (queue, key) IN (
				SELECT queue, key FROM claim_queue
				WHERE queue = $3
				  AND (not_before IS NULL OR not_before <= now())
				ORDER BY not_before NULLS FIRST, priority DESC, created_at ASC, key ASC
				FOR UPDATE SKIP LOCKED
				LIMIT $4
			)
			RETURNING key
		),
		claimed AS (
			UPDATE work_items
			SET status = 'claimed',
				worker_id = $1,
				lease_expires = now() + $2::interval,
				attempts = attempts + 1,
				claimed_at = now(),
				updated_at = now()
			WHERE queue = $3 AND key IN (SELECT key FROM claimed_keys)
			RETURNING queue, key, status, priority, attempts, max_attempts,
				not_before, lease_expires, worker_id, error_message,
				created_at, updated_at, claimed_at, completed_at
		),
		insert_leases AS (
			INSERT INTO active_leases (queue, key, worker_id, lease_expires)
			SELECT queue, key, worker_id, lease_expires FROM claimed
		),
		insert_history AS (
			INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, attempt)
			SELECT queue, key, 'pending', 'claimed', worker_id, attempts FROM claimed
		)
		SELECT queue, key, status, priority, attempts, max_attempts,
			not_before, lease_expires, worker_id, error_message,
			created_at, updated_at, claimed_at, completed_at
		FROM claimed
	`, workerID, leaseDuration.String(), queue, limit)
	if err != nil {
		return nil, fmt.Errorf("claim query: %w", err)
	}
	defer rows.Close()

	var items []store.WorkItem
	for rows.Next() {
		var item store.WorkItem
		if err := scanWorkItem(rows, &item); err != nil {
			return nil, fmt.Errorf("scan claimed item: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}

	// RETURNING doesn't preserve subquery ORDER BY, so sort explicitly.
	sort.Slice(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority > items[j].Priority
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Key < items[j].Key
	})

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return items, nil
}

func (s *Store) Complete(ctx context.Context, queue, key string) error {
	tag, err := s.pool.Exec(ctx, `
		WITH completed AS (
			UPDATE work_items
			SET status = 'succeeded',
				completed_at = now(),
				updated_at = now(),
				lease_expires = NULL
			WHERE queue = $1 AND key = $2
			  AND status IN ('claimed', 'running')
			RETURNING queue, key
		),
		delete_lease AS (
			DELETE FROM active_leases
			WHERE queue = $1 AND key = $2
			  AND EXISTS (SELECT 1 FROM completed)
		)
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		SELECT queue, key, 'running', 'succeeded' FROM completed
	`, queue, key)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, queue, key string, errMsg string) error {
	// No in_progress decrement — Fail() is always followed by Requeue() or
	// Deadletter(), which handle the decrement. Decrementing in both places
	// would double-decrement the counter.
	tag, err := s.pool.Exec(ctx, `
		WITH failed AS (
			UPDATE work_items
			SET status = 'failed',
				error_message = NULLIF($3, ''),
				completed_at = now(),
				updated_at = now(),
				lease_expires = NULL
			WHERE queue = $1 AND key = $2
			  AND status IN ('claimed', 'running')
			RETURNING queue, key
		),
		delete_lease AS (
			DELETE FROM active_leases
			WHERE queue = $1 AND key = $2
			  AND EXISTS (SELECT 1 FROM failed)
		)
		INSERT INTO work_item_history (queue, key, from_status, to_status, error_message)
		SELECT queue, key, 'running', 'failed', NULLIF($3, '') FROM failed
	`, queue, key, errMsg)
	if err != nil {
		return fmt.Errorf("fail: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) Requeue(ctx context.Context, queue, key string, opts ...store.RequeueOption) error {
	o := store.ApplyRequeueOptions(opts)

	tag, err := s.pool.Exec(ctx, `
		WITH requeued AS (
			UPDATE work_items
			SET status = 'pending',
				not_before = $3,
				worker_id = NULL,
				lease_expires = NULL,
				error_message = NULL,
				updated_at = now(),
				claimed_at = NULL,
				completed_at = NULL
			WHERE queue = $1 AND key = $2
			  AND status IN ('claimed', 'running', 'failed')
			RETURNING queue, key, priority, not_before, created_at
		),
		delete_lease AS (
			DELETE FROM active_leases
			WHERE queue = $1 AND key = $2
			  AND EXISTS (SELECT 1 FROM requeued)
		),
		insert_claim AS (
			INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
			SELECT queue, key, priority, not_before, created_at FROM requeued
			ON CONFLICT (queue, key) DO NOTHING
		)
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		SELECT queue, key, 'running', 'pending' FROM requeued
	`, queue, key, o.NotBefore)
	if err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		WITH requeued AS (
			UPDATE work_items
			SET status = 'pending',
				attempts = GREATEST(attempts - 1, 0),
				not_before = $3,
				worker_id = NULL,
				lease_expires = NULL,
				error_message = NULL,
				updated_at = now(),
				claimed_at = NULL,
				completed_at = NULL
			WHERE queue = $1 AND key = $2
			  AND status IN ('claimed', 'running')
			RETURNING queue, key, priority, not_before, created_at
		),
		delete_lease AS (
			DELETE FROM active_leases
			WHERE queue = $1 AND key = $2
			  AND EXISTS (SELECT 1 FROM requeued)
		),
		insert_claim AS (
			INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
			SELECT queue, key, priority, not_before, created_at FROM requeued
			ON CONFLICT (queue, key) DO UPDATE SET not_before = EXCLUDED.not_before
		)
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		SELECT queue, key, 'claimed', 'pending' FROM requeued
	`, queue, key, notBefore)
	if err != nil {
		return fmt.Errorf("requeue undo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) Deadletter(ctx context.Context, queue, key string) error {
	tag, err := s.pool.Exec(ctx, `
		WITH dead AS (
			UPDATE work_items
			SET status = 'dead_letter',
				completed_at = now(),
				updated_at = now(),
				lease_expires = NULL
			WHERE queue = $1 AND key = $2
			  AND status IN ('claimed', 'running', 'failed')
			RETURNING queue, key
		),
		delete_lease AS (
			DELETE FROM active_leases
			WHERE queue = $1 AND key = $2
			  AND EXISTS (SELECT 1 FROM dead)
		)
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		SELECT queue, key, 'failed', 'dead_letter' FROM dead
	`, queue, key)
	if err != nil {
		return fmt.Errorf("deadletter: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE work_items
		SET lease_expires = now() + $3::interval,
			updated_at = now()
		WHERE queue = $1 AND key = $2
		  AND status IN ('claimed', 'running')
	`, queue, key, duration.String())
	if err != nil {
		return fmt.Errorf("extend lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE active_leases SET lease_expires = now() + $3::interval
		WHERE queue = $1 AND key = $2
	`, queue, key, duration.String())
	if err != nil {
		return fmt.Errorf("extend active_lease: %w", err)
	}
	return nil
}

func (s *Store) Transition(ctx context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	o := store.ApplyTransitionOptions(opts)
	if !store.ValidTransition(from, to) {
		return store.ErrInvalidTransition
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus store.Status
	err = tx.QueryRow(ctx, `
		SELECT status FROM work_items
		WHERE queue = $1 AND key = $2
		FOR UPDATE
	`, queue, key).Scan(&currentStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return store.ErrNotFound
		}
		return fmt.Errorf("read status: %w", err)
	}

	if currentStatus != from {
		return store.ErrConflict
	}

	_, err = tx.Exec(ctx, `
		UPDATE work_items
		SET status = $3,
			worker_id = COALESCE(NULLIF($4, ''), worker_id),
			error_message = COALESCE(NULLIF($5, ''), error_message),
			updated_at = now()
		WHERE queue = $1 AND key = $2
	`, queue, key, to, o.WorkerID, o.ErrorMessage)
	if err != nil {
		return fmt.Errorf("transition: %w", err)
	}

	if to == store.StatusPending {
		_, err = tx.Exec(ctx, `
			INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
			SELECT queue, key, priority, not_before, created_at
			FROM work_items WHERE queue = $1 AND key = $2
			ON CONFLICT (queue, key) DO NOTHING
		`, queue, key)
		if err != nil {
			return fmt.Errorf("insert claim_queue: %w", err)
		}
	} else if from == store.StatusPending {
		_, err = tx.Exec(ctx, `
			DELETE FROM claim_queue WHERE queue = $1 AND key = $2
		`, queue, key)
		if err != nil {
			return fmt.Errorf("delete claim_queue: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, error_message)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
	`, queue, key, from, to, o.WorkerID, o.ErrorMessage)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

// --- Queue Management ---

func (s *Store) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO queue_state (queue, max_concurrency, max_retry, compute_backend)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (queue) DO UPDATE SET
			max_concurrency = EXCLUDED.max_concurrency,
			max_retry = EXCLUDED.max_retry,
			compute_backend = EXCLUDED.compute_backend
	`, queue, cfg.MaxConcurrency, cfg.MaxRetry, cfg.ComputeBackend)
	return err
}

func (s *Store) SetQueuePaused(ctx context.Context, queue string, paused bool) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE queue_state SET paused = $2 WHERE queue = $1
	`, queue, paused)
	return err
}

func (s *Store) IsQueuePaused(ctx context.Context, queue string) (bool, error) {
	var paused bool
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(paused, false) FROM queue_state WHERE queue = $1
	`, queue).Scan(&paused)
	if err != nil {
		return false, nil // queue doesn't exist yet, not paused
	}
	return paused, nil
}

func (s *Store) RepairCounter(ctx context.Context, queue string) error {
	// Update the informational in_progress counter from active_leases,
	// which is the authoritative source for concurrency.
	_, err := s.pool.Exec(ctx, `
		UPDATE queue_state
		SET in_progress = (
			SELECT COUNT(*) FROM active_leases
			WHERE queue = $1
		)
		WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("repair in_progress: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		DELETE FROM claim_queue
		WHERE queue = $1
		  AND (queue, key) NOT IN (
			SELECT queue, key FROM work_items
			WHERE queue = $1 AND status = 'pending'
		  )
	`, queue)
	if err != nil {
		return fmt.Errorf("repair claim_queue orphans: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
		SELECT queue, key, priority, not_before, created_at
		FROM work_items
		WHERE queue = $1 AND status = 'pending'
		ON CONFLICT (queue, key) DO NOTHING
	`, queue)
	if err != nil {
		return fmt.Errorf("repair claim_queue backfill: %w", err)
	}

	return nil
}

// --- Query Operations ---

func (s *Store) CountByStatus(ctx context.Context, queue string, statuses ...store.Status) (map[store.Status]int64, error) {
	query := `SELECT status, COUNT(*) FROM work_items WHERE queue = $1`
	args := []any{queue}

	if len(statuses) > 0 {
		placeholders := make([]string, len(statuses))
		for i, st := range statuses {
			placeholders[i] = fmt.Sprintf("$%d", i+2)
			args = append(args, string(st))
		}
		query += " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	query += " GROUP BY status"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[store.Status]int64)
	for rows.Next() {
		var status store.Status
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	query := `
		SELECT queue, key, status, priority, attempts, max_attempts,
			not_before, lease_expires, worker_id, error_message,
			created_at, updated_at, claimed_at, completed_at
		FROM work_items
		WHERE queue = $1
	`
	args := []any{filter.Queue}
	argIdx := 2

	if filter.Status != nil {
		query += fmt.Sprintf(" AND status = $%d", argIdx)
		args = append(args, *filter.Status)
		argIdx++
	}

	query += " ORDER BY priority DESC, created_at ASC, key ASC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, limit)
	argIdx++

	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", argIdx)
		args = append(args, filter.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	var items []store.WorkItem
	for rows.Next() {
		var item store.WorkItem
		if err := scanWorkItem(rows, &item); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListExpiredLeases(ctx context.Context, queue string, limit int) ([]store.WorkItem, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT w.queue, w.key, w.status, w.priority, w.attempts, w.max_attempts,
			w.not_before, w.lease_expires, w.worker_id, w.error_message,
			w.created_at, w.updated_at, w.claimed_at, w.completed_at
		FROM active_leases a
		JOIN work_items w ON w.queue = a.queue AND w.key = a.key
		WHERE a.queue = $1 AND a.lease_expires < now()
		ORDER BY a.lease_expires ASC
		LIMIT $2
	`, queue, limit)
	if err != nil {
		return nil, fmt.Errorf("list expired leases: %w", err)
	}
	defer rows.Close()

	var items []store.WorkItem
	for rows.Next() {
		var item store.WorkItem
		if err := scanWorkItem(rows, &item); err != nil {
			return nil, fmt.Errorf("scan expired item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetItem(ctx context.Context, queue, key string) (*store.WorkItem, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT queue, key, status, priority, attempts, max_attempts,
			not_before, lease_expires, worker_id, error_message,
			created_at, updated_at, claimed_at, completed_at
		FROM work_items
		WHERE queue = $1 AND key = $2
	`, queue, key)

	var item store.WorkItem
	if err := scanWorkItem(row, &item); err != nil {
		if err == pgx.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get item: %w", err)
	}
	return &item, nil
}

// --- Admin Queries ---

func (s *Store) ListQueues(ctx context.Context) ([]store.QueueInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT queue, max_concurrency, max_retry, compute_backend, COALESCE(paused, false), in_progress
		FROM queue_state ORDER BY queue
	`)
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	defer rows.Close()

	var queues []store.QueueInfo
	for rows.Next() {
		var qi store.QueueInfo
		if err := rows.Scan(&qi.Name, &qi.MaxConcurrency, &qi.MaxRetry, &qi.ComputeBackend, &qi.Paused, &qi.InProgress); err != nil {
			return nil, fmt.Errorf("scan queue: %w", err)
		}
		qi.Counts = make(map[string]int)
		queues = append(queues, qi)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	countRows, err := s.pool.Query(ctx, `
		SELECT queue, status, COUNT(*)::int FROM work_items GROUP BY queue, status
	`)
	if err != nil {
		return nil, fmt.Errorf("count items: %w", err)
	}
	defer countRows.Close()

	countMap := make(map[string]map[string]int)
	for countRows.Next() {
		var queue, status string
		var count int
		if err := countRows.Scan(&queue, &status, &count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		if countMap[queue] == nil {
			countMap[queue] = make(map[string]int)
		}
		countMap[queue][status] = count
	}
	if err := countRows.Err(); err != nil {
		return nil, err
	}

	for i := range queues {
		if counts, ok := countMap[queues[i].Name]; ok {
			queues[i].Counts = counts
		}
	}

	return queues, nil
}

func (s *Store) ListWorkers(ctx context.Context, queue string) ([]store.WorkerLease, error) {
	query := `
		SELECT worker_id, queue, compute_backend, COALESCE(hostname, ''),
			started_at, last_heartbeat, items_processed, status
		FROM worker_leases
	`
	var args []any
	if queue != "" {
		query += " WHERE queue = $1"
		args = append(args, queue)
	}
	query += " ORDER BY queue, worker_id"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer rows.Close()

	var workers []store.WorkerLease
	for rows.Next() {
		var w store.WorkerLease
		if err := rows.Scan(&w.WorkerID, &w.Queue, &w.ComputeBackend, &w.Hostname,
			&w.StartedAt, &w.LastHeartbeat, &w.ItemsProcessed, &w.Status); err != nil {
			return nil, fmt.Errorf("scan worker: %w", err)
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

func (s *Store) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	const batchSize = 1000
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM work_items
			WHERE ctid IN (
				SELECT ctid FROM work_items
				WHERE queue = $1 AND status = 'dead_letter'
				LIMIT $2
			)
		`, queue, batchSize)
		if err != nil {
			return total, fmt.Errorf("purge dead letters: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < batchSize {
			break
		}
	}
	return total, nil
}

// --- History ---

func (s *Store) RecordHistory(ctx context.Context, entry store.HistoryEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO work_item_history
			(queue, key, from_status, to_status, worker_id, error_message, attempt, trace_id)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''), $7, NULLIF($8, ''))
	`, entry.Queue, entry.Key, entry.FromStatus, entry.ToStatus,
		entry.WorkerID, entry.ErrorMessage, entry.Attempt, entry.TraceID)
	return err
}

func (s *Store) GetItemHistory(ctx context.Context, queue, key string) ([]store.HistoryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, queue, key, COALESCE(from_status, ''), to_status,
			COALESCE(worker_id, ''), COALESCE(error_message, ''),
			COALESCE(attempt, 0), COALESCE(trace_id, ''), created_at
		FROM work_item_history
		WHERE queue = $1 AND key = $2
		ORDER BY created_at DESC
		LIMIT 100
	`, queue, key)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer rows.Close()

	var entries []store.HistoryEntry
	for rows.Next() {
		var e store.HistoryEntry
		if err := rows.Scan(&e.ID, &e.Queue, &e.Key, &e.FromStatus, &e.ToStatus,
			&e.WorkerID, &e.ErrorMessage, &e.Attempt, &e.TraceID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn: %w", err)
	}

	channel := pgx.Identifier{"work_item_" + queue}.Sanitize()
	_, err = conn.Exec(ctx, "LISTEN "+channel)
	if err != nil {
		conn.Release()
		return nil, fmt.Errorf("listen: %w", err)
	}

	ch := make(chan store.Event, 64)
	go func() {
		defer conn.Release()
		defer close(ch)
		for {
			notification, err := conn.Conn().WaitForNotification(ctx)
			if err != nil {
				return
			}
			var event store.Event
			if json.Unmarshal([]byte(notification.Payload), &event) == nil {
				event.Queue = queue
				select {
				case ch <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// --- Leader Election ---

func (s *Store) TryLeader(ctx context.Context, queue, workerID string, ttl time.Duration) (bool, error) {
	var leaderID string
	err := s.pool.QueryRow(ctx, `
		UPDATE queue_state
		SET leader_id = $1, leader_expires = now() + $2::interval
		WHERE queue = $3
		  AND (leader_id IS NULL OR leader_id = $1 OR leader_expires < now())
		RETURNING leader_id
	`, workerID, ttl.String(), queue).Scan(&leaderID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("try leader: %w", err)
	}
	return leaderID == workerID, nil
}

// --- Health ---

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// --- Helpers ---

type scannable interface {
	Scan(dest ...any) error
}

func scanWorkItem(row scannable, item *store.WorkItem) error {
	var workerID, errorMessage *string
	err := row.Scan(
		&item.Queue, &item.Key, &item.Status, &item.Priority,
		&item.Attempts, &item.MaxAttempts, &item.NotBefore,
		&item.LeaseExpires, &workerID, &errorMessage,
		&item.CreatedAt, &item.UpdatedAt, &item.ClaimedAt, &item.CompletedAt,
	)
	if err != nil {
		return err
	}
	if workerID != nil {
		item.WorkerID = *workerID
	}
	if errorMessage != nil {
		item.ErrorMessage = *errorMessage
	}
	return nil
}

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)
