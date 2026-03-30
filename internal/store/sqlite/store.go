// Package sqlite implements store.Interface backed by SQLite.
//
// Uses the same SQL schema as the PostgreSQL backend (with minor syntax
// adjustments). Suitable for single-node deployments, edge/satellite
// workers, development, and testing with durability.
//
// Concurrency: SQLite uses file-level locking. WAL mode allows concurrent
// reads with a single writer. ClaimBatch serializes via a BEGIN IMMEDIATE
// transaction (equivalent to SKIP LOCKED in a single-process context).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hummingbird-org/factory/internal/store"
)

// Store implements store.Interface using SQLite.
type Store struct {
	db *sql.DB

	// mu serializes write transactions to avoid SQLITE_BUSY errors.
	mu sync.Mutex

	// Event subscribers.
	subMu sync.Mutex
	subs  map[string][]chan store.Event
}

// New opens (or creates) a SQLite database at the given path.
// Use ":memory:" for an in-memory database.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable WAL mode for concurrent reads.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	s := &Store{db: db, subs: make(map[string][]chan store.Event)}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS work_items (
			queue           TEXT NOT NULL,
			key             TEXT NOT NULL,
			status          TEXT NOT NULL DEFAULT 'pending',
			priority        INTEGER NOT NULL DEFAULT 0,
			attempts        INTEGER NOT NULL DEFAULT 0,
			max_attempts    INTEGER NOT NULL DEFAULT 5,
			not_before      TEXT,
			lease_expires   TEXT,
			worker_id       TEXT,
			error_message   TEXT,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			claimed_at      TEXT,
			completed_at    TEXT,
			PRIMARY KEY (queue, key)
		);

		CREATE INDEX IF NOT EXISTS idx_work_items_claimable
			ON work_items (queue, priority DESC, created_at ASC)
			WHERE status = 'pending';

		CREATE INDEX IF NOT EXISTS idx_work_items_queue_status
			ON work_items (queue, status);

		CREATE TABLE IF NOT EXISTS work_item_history (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			queue           TEXT NOT NULL,
			key             TEXT NOT NULL,
			from_status     TEXT,
			to_status       TEXT NOT NULL,
			worker_id       TEXT,
			error_message   TEXT,
			attempt         INTEGER,
			trace_id        TEXT,
			created_at      TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_history_queue_key
			ON work_item_history (queue, key, created_at DESC);

		CREATE TABLE IF NOT EXISTS worker_leases (
			worker_id       TEXT PRIMARY KEY,
			queue           TEXT NOT NULL,
			compute_backend TEXT NOT NULL,
			hostname        TEXT,
			started_at      TEXT NOT NULL,
			last_heartbeat  TEXT NOT NULL,
			items_processed INTEGER NOT NULL DEFAULT 0,
			status          TEXT NOT NULL DEFAULT 'active'
		);

		CREATE TABLE IF NOT EXISTS queue_state (
			queue           TEXT PRIMARY KEY,
			max_concurrency INTEGER NOT NULL DEFAULT 10,
			max_retry       INTEGER NOT NULL DEFAULT 5,
			compute_backend TEXT NOT NULL DEFAULT 'kubernetes',
			in_progress     INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}

// --- Time helpers (SQLite stores as TEXT in RFC3339Nano) ---

func timeStr(t time.Time) string { return t.Format(time.RFC3339Nano) }

func timePtrStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseTimePtr(s *string) *time.Time {
	if s == nil || *s == "" {
		return nil
	}
	t := parseTime(*s)
	return &t
}

func scanWorkItem(scanner interface {
	Scan(dest ...any) error
}) (store.WorkItem, error) {
	var item store.WorkItem
	var status, createdAt, updatedAt string
	var notBefore, leaseExpires, workerID, errorMessage, claimedAt, completedAt *string

	err := scanner.Scan(
		&item.Queue, &item.Key, &status, &item.Priority,
		&item.Attempts, &item.MaxAttempts,
		&notBefore, &leaseExpires, &workerID, &errorMessage,
		&createdAt, &updatedAt, &claimedAt, &completedAt,
	)
	if err != nil {
		return item, err
	}

	item.Status = store.Status(status)
	item.CreatedAt = parseTime(createdAt)
	item.UpdatedAt = parseTime(updatedAt)
	item.NotBefore = parseTimePtr(notBefore)
	item.LeaseExpires = parseTimePtr(leaseExpires)
	if workerID != nil {
		item.WorkerID = *workerID
	}
	if errorMessage != nil {
		item.ErrorMessage = *errorMessage
	}
	item.ClaimedAt = parseTimePtr(claimedAt)
	item.CompletedAt = parseTimePtr(completedAt)
	return item, nil
}

const selectCols = `queue, key, status, priority, attempts, max_attempts,
	not_before, lease_expires, worker_id, error_message,
	created_at, updated_at, claimed_at, completed_at`

// --- store.Interface ---

func (s *Store) Enqueue(ctx context.Context, queue, key string, priority int, opts ...store.EnqueueOption) error {
	o := store.ApplyEnqueueOptions(opts)
	now := timeStr(time.Now())

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO work_items (queue, key, priority, not_before, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (queue, key) DO UPDATE SET
			priority = CASE
				WHEN work_items.status = 'pending'
				THEN MAX(work_items.priority, excluded.priority)
				ELSE excluded.priority
			END,
			status = 'pending',
			attempts = CASE WHEN work_items.status = 'pending' THEN work_items.attempts ELSE 0 END,
			not_before = excluded.not_before,
			worker_id = NULL,
			lease_expires = NULL,
			error_message = NULL,
			claimed_at = NULL,
			completed_at = NULL,
			updated_at = ?
		WHERE work_items.status IN ('pending', 'succeeded', 'failed', 'dead_letter')
	`, queue, key, priority, timePtrStr(o.NotBefore), now, now, now)

	s.emit(store.Event{Queue: queue, Key: key, Status: "pending", Priority: priority})
	return err
}

func (s *Store) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]store.WorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var inProgress, maxConc int
	err = tx.QueryRowContext(ctx, `
		SELECT in_progress, max_concurrency FROM queue_state WHERE queue = ?
	`, queue).Scan(&inProgress, &maxConc)
	if err != nil {
		return nil, fmt.Errorf("read queue_state: %w", err)
	}

	remaining := maxConc - inProgress
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	now := time.Now()
	nowStr := timeStr(now)
	leaseExp := timeStr(now.Add(leaseDuration))

	rows, err := tx.QueryContext(ctx, `
		SELECT `+selectCols+` FROM work_items
		WHERE queue = ? AND status = 'pending'
		  AND (not_before IS NULL OR not_before <= ?)
		ORDER BY priority DESC, created_at ASC, key ASC
		LIMIT ?
	`, queue, nowStr, limit)
	if err != nil {
		return nil, err
	}

	var keys []string
	var items []store.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, item.Key)
		item.Status = store.StatusClaimed
		item.WorkerID = workerID
		item.Attempts++
		item.LeaseExpires = parseTimePtr(&leaseExp)
		item.ClaimedAt = &now
		item.UpdatedAt = now
		items = append(items, item)
	}
	rows.Close()

	if len(items) == 0 {
		return nil, nil
	}

	for _, item := range items {
		_, err = tx.ExecContext(ctx, `
			UPDATE work_items
			SET status = 'claimed', worker_id = ?, lease_expires = ?,
				attempts = attempts + 1, claimed_at = ?, updated_at = ?
			WHERE queue = ? AND key = ? AND status = 'pending'
		`, workerID, leaseExp, nowStr, nowStr, queue, item.Key)
		if err != nil {
			return nil, err
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, attempt, created_at)
			VALUES (?, ?, 'pending', 'claimed', ?, ?, ?)
		`, queue, item.Key, workerID, item.Attempts, nowStr)
		if err != nil {
			return nil, err
		}
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE queue_state SET in_progress = in_progress + ? WHERE queue = ?
	`, len(items), queue)
	if err != nil {
		return nil, err
	}

	return items, tx.Commit()
}

func (s *Store) Complete(ctx context.Context, queue, key string) error {
	return s.setTerminal(ctx, queue, key, "succeeded", "")
}

func (s *Store) Fail(ctx context.Context, queue, key string, errMsg string) error {
	return s.setTerminal(ctx, queue, key, "failed", errMsg)
}

func (s *Store) setTerminal(ctx context.Context, queue, key, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var errMsgPtr *string
	if errMsg != "" {
		errMsgPtr = &errMsg
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = ?, error_message = ?, completed_at = ?, updated_at = ?, lease_expires = NULL
		WHERE queue = ? AND key = ? AND status IN ('claimed', 'running')
	`, status, errMsgPtr, now, now, queue, key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}

	tx.ExecContext(ctx, `
		UPDATE queue_state SET in_progress = MAX(in_progress - 1, 0) WHERE queue = ?
	`, queue)

	tx.ExecContext(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, error_message, created_at)
		VALUES (?, ?, 'running', ?, ?, ?)
	`, queue, key, status, errMsgPtr, now)

	s.emit(store.Event{Queue: queue, Key: key, Status: status})
	return tx.Commit()
}

func (s *Store) Requeue(ctx context.Context, queue, key string, opts ...store.RequeueOption) error {
	o := store.ApplyRequeueOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = 'pending', not_before = ?, worker_id = NULL, lease_expires = NULL,
			error_message = NULL, claimed_at = NULL, completed_at = NULL, updated_at = ?
		WHERE queue = ? AND key = ? AND status IN ('claimed', 'running', 'failed')
	`, timePtrStr(o.NotBefore), now, queue, key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}

	tx.ExecContext(ctx, `UPDATE queue_state SET in_progress = MAX(in_progress - 1, 0) WHERE queue = ?`, queue)
	tx.ExecContext(ctx, `INSERT INTO work_item_history (queue, key, from_status, to_status, created_at) VALUES (?, ?, 'running', 'pending', ?)`, queue, key, now)

	return tx.Commit()
}

func (s *Store) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())
	nb := timeStr(notBefore)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = 'pending', attempts = MAX(attempts - 1, 0), not_before = ?,
			worker_id = NULL, lease_expires = NULL, error_message = NULL,
			claimed_at = NULL, completed_at = NULL, updated_at = ?
		WHERE queue = ? AND key = ? AND status IN ('claimed', 'running')
	`, nb, now, queue, key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}

	tx.ExecContext(ctx, `UPDATE queue_state SET in_progress = MAX(in_progress - 1, 0) WHERE queue = ?`, queue)
	tx.ExecContext(ctx, `INSERT INTO work_item_history (queue, key, from_status, to_status, created_at) VALUES (?, ?, 'claimed', 'pending', ?)`, queue, key, now)

	return tx.Commit()
}

func (s *Store) Deadletter(ctx context.Context, queue, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = 'dead_letter', completed_at = ?, updated_at = ?, lease_expires = NULL
		WHERE queue = ? AND key = ? AND status IN ('claimed', 'running', 'failed')
	`, now, now, queue, key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}

	tx.ExecContext(ctx, `UPDATE queue_state SET in_progress = MAX(in_progress - 1, 0) WHERE queue = ?`, queue)
	tx.ExecContext(ctx, `INSERT INTO work_item_history (queue, key, from_status, to_status, created_at) VALUES (?, ?, 'failed', 'dead_letter', ?)`, queue, key, now)

	s.emit(store.Event{Queue: queue, Key: key, Status: "dead_letter"})
	return tx.Commit()
}

func (s *Store) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	exp := timeStr(time.Now().Add(duration))
	now := timeStr(time.Now())

	result, err := s.db.ExecContext(ctx, `
		UPDATE work_items SET lease_expires = ?, updated_at = ?
		WHERE queue = ? AND key = ? AND status IN ('claimed', 'running')
	`, exp, now, queue, key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) Transition(ctx context.Context, queue, key string, from, to store.Status, opts ...store.TransitionOption) error {
	o := store.ApplyTransitionOptions(opts)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Check current status.
	var currentStatus string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM work_items WHERE queue = ? AND key = ?
	`, queue, key).Scan(&currentStatus)
	if err == sql.ErrNoRows {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentStatus != string(from) {
		return store.ErrConflict
	}

	workerID := o.WorkerID
	errMsg := o.ErrorMessage

	tx.ExecContext(ctx, `
		UPDATE work_items
		SET status = ?,
			worker_id = COALESCE(NULLIF(?, ''), worker_id),
			error_message = COALESCE(NULLIF(?, ''), error_message),
			updated_at = ?
		WHERE queue = ? AND key = ?
	`, to, workerID, errMsg, now, queue, key)

	tx.ExecContext(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, error_message, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
	`, queue, key, from, to, workerID, errMsg, now)

	s.emit(store.Event{Queue: queue, Key: key, Status: string(to)})
	return tx.Commit()
}

// --- Queue Management ---

func (s *Store) EnsureQueue(ctx context.Context, queue string, cfg store.QueueConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO queue_state (queue, max_concurrency, max_retry, compute_backend)
		VALUES (?, ?, ?, ?)
	`, queue, cfg.MaxConcurrency, cfg.MaxRetry, cfg.ComputeBackend)
	return err
}

func (s *Store) RepairCounter(ctx context.Context, queue string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		UPDATE queue_state SET in_progress = (
			SELECT COUNT(*) FROM work_items
			WHERE queue = ? AND status IN ('claimed', 'running')
		) WHERE queue = ?
	`, queue, queue)
	return err
}

// --- Query Operations ---

func (s *Store) CountByStatus(ctx context.Context, queue string) (map[store.Status]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM work_items WHERE queue = ? GROUP BY status
	`, queue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[store.Status]int64)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[store.Status(status)] = count
	}
	return counts, nil
}

func (s *Store) List(ctx context.Context, filter store.ListFilter) ([]store.WorkItem, error) {
	query := "SELECT " + selectCols + " FROM work_items WHERE queue = ?"
	args := []any{filter.Queue}

	if filter.Status != nil {
		query += " AND status = ?"
		args = append(args, string(*filter.Status))
	}

	query += " ORDER BY priority DESC, created_at ASC, key ASC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += " LIMIT ?"
	args = append(args, limit)

	if filter.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, filter.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []store.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) GetItem(ctx context.Context, queue, key string) (*store.WorkItem, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+selectCols+" FROM work_items WHERE queue = ? AND key = ?", queue, key)
	item, err := scanWorkItem(row)
	if err == sql.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

// --- Admin Queries ---

func (s *Store) ListQueues(ctx context.Context) ([]store.QueueInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT queue, max_concurrency, max_retry, compute_backend, in_progress
		FROM queue_state ORDER BY queue
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var queues []store.QueueInfo
	for rows.Next() {
		var qi store.QueueInfo
		if err := rows.Scan(&qi.Name, &qi.MaxConcurrency, &qi.MaxRetry, &qi.ComputeBackend, &qi.InProgress); err != nil {
			return nil, err
		}
		qi.Counts = make(map[string]int)
		queues = append(queues, qi)
	}

	for i := range queues {
		crows, _ := s.db.QueryContext(ctx, `
			SELECT status, COUNT(*) FROM work_items WHERE queue = ? GROUP BY status
		`, queues[i].Name)
		if crows != nil {
			for crows.Next() {
				var status string
				var count int
				crows.Scan(&status, &count)
				queues[i].Counts[status] = count
			}
			crows.Close()
		}
	}

	return queues, nil
}

func (s *Store) ListWorkers(ctx context.Context, queue string) ([]store.WorkerLease, error) {
	query := `SELECT worker_id, queue, compute_backend, COALESCE(hostname, ''),
		started_at, last_heartbeat, items_processed, status FROM worker_leases`
	var args []any
	if queue != "" {
		query += " WHERE queue = ?"
		args = append(args, queue)
	}
	query += " ORDER BY queue, worker_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var workers []store.WorkerLease
	for rows.Next() {
		var w store.WorkerLease
		var startedAt, lastHB string
		if err := rows.Scan(&w.WorkerID, &w.Queue, &w.ComputeBackend, &w.Hostname,
			&startedAt, &lastHB, &w.ItemsProcessed, &w.Status); err != nil {
			return nil, err
		}
		w.StartedAt = parseTime(startedAt)
		w.LastHeartbeat = parseTime(lastHB)
		workers = append(workers, w)
	}
	return workers, nil
}

func (s *Store) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.ExecContext(ctx, `
		DELETE FROM work_items WHERE queue = ? AND status = 'dead_letter'
	`, queue)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// --- History ---

func (s *Store) RecordHistory(ctx context.Context, entry store.HistoryEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := timeStr(time.Now())
	if !entry.CreatedAt.IsZero() {
		now = timeStr(entry.CreatedAt)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, error_message, attempt, trace_id, created_at)
		VALUES (?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?)
	`, entry.Queue, entry.Key, entry.FromStatus, entry.ToStatus,
		entry.WorkerID, entry.ErrorMessage, entry.Attempt, entry.TraceID, now)
	return err
}

func (s *Store) GetItemHistory(ctx context.Context, queue, key string) ([]store.HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, queue, key, COALESCE(from_status, ''), to_status,
			COALESCE(worker_id, ''), COALESCE(error_message, ''),
			COALESCE(attempt, 0), COALESCE(trace_id, ''), created_at
		FROM work_item_history
		WHERE queue = ? AND key = ?
		ORDER BY created_at DESC
		LIMIT 100
	`, queue, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []store.HistoryEntry
	for rows.Next() {
		var e store.HistoryEntry
		var createdAt string
		if err := rows.Scan(&e.ID, &e.Queue, &e.Key, &e.FromStatus, &e.ToStatus,
			&e.WorkerID, &e.ErrorMessage, &e.Attempt, &e.TraceID, &createdAt); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(createdAt)
		entries = append(entries, e)
	}
	return entries, nil
}

// --- Events ---

func (s *Store) Subscribe(ctx context.Context, queue string) (<-chan store.Event, error) {
	s.subMu.Lock()
	ch := make(chan store.Event, 64)
	s.subs[queue] = append(s.subs[queue], ch)
	s.subMu.Unlock()

	go func() {
		<-ctx.Done()
		s.subMu.Lock()
		defer s.subMu.Unlock()
		subs := s.subs[queue]
		for i, sub := range subs {
			if sub == ch {
				s.subs[queue] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}()

	return ch, nil
}

func (s *Store) emit(event store.Event) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for _, ch := range s.subs[event.Queue] {
		select {
		case ch <- event:
		default:
		}
	}
}

// Verify interface compliance.
var _ store.Interface = (*Store)(nil)
