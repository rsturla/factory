// Package postgres implements the workqueue.Interface backed by PostgreSQL.
//
// It uses SELECT FOR UPDATE SKIP LOCKED for zero-contention job claiming,
// upsert with GREATEST for priority-merging deduplication, and a meta row
// in queue_state for O(1) concurrency tracking.
package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

//go:embed schema.sql
var schemaSQL string

// Client implements workqueue.Interface using PostgreSQL.
type Client struct {
	pool *pgxpool.Pool
}

// New creates a new PostgreSQL workqueue client.
func New(pool *pgxpool.Pool) *Client {
	return &Client{pool: pool}
}

// Migrate applies the workqueue schema to the database.
func (c *Client) Migrate(ctx context.Context) error {
	_, err := c.pool.Exec(ctx, schemaSQL)
	return err
}

func (c *Client) Enqueue(ctx context.Context, queue, key string, priority int, opts ...workqueue.EnqueueOption) error {
	o := workqueue.ApplyEnqueueOptions(opts)
	_, err := c.pool.Exec(ctx, `
		INSERT INTO work_items (queue, key, priority, not_before)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (queue, key) DO UPDATE SET
			priority = GREATEST(work_items.priority, EXCLUDED.priority),
			updated_at = now()
		WHERE work_items.status = 'pending'
	`, queue, key, priority, o.NotBefore)
	return err
}

func (c *Client) ClaimBatch(ctx context.Context, queue string, batchSize int, workerID string, leaseDuration time.Duration) ([]workqueue.WorkItem, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Check concurrency headroom via queue_state meta row.
	var inProgress, maxConc int
	err = tx.QueryRow(ctx, `
		SELECT in_progress, max_concurrency FROM queue_state
		WHERE queue = $1 FOR UPDATE
	`, queue).Scan(&inProgress, &maxConc)
	if err != nil {
		return nil, fmt.Errorf("read queue_state: %w", err)
	}

	remaining := maxConc - inProgress
	if remaining <= 0 {
		return nil, nil
	}
	limit := min(batchSize, remaining)

	// Claim items with SKIP LOCKED — zero contention between concurrent workers.
	rows, err := tx.Query(ctx, `
		UPDATE work_items
		SET status = 'claimed',
			worker_id = $1,
			lease_expires = now() + $2::interval,
			attempts = attempts + 1,
			claimed_at = now(),
			updated_at = now()
		WHERE (queue, key) IN (
			SELECT queue, key FROM work_items
			WHERE queue = $3
			  AND status = 'pending'
			  AND (not_before IS NULL OR not_before <= now())
			ORDER BY priority DESC, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $4
		)
		RETURNING queue, key, status, priority, attempts, max_attempts,
			not_before, lease_expires, worker_id, error_message,
			created_at, updated_at, claimed_at, completed_at
	`, workerID, leaseDuration.String(), queue, limit)
	if err != nil {
		return nil, fmt.Errorf("claim query: %w", err)
	}
	defer rows.Close()

	var items []workqueue.WorkItem
	for rows.Next() {
		var item workqueue.WorkItem
		err := rows.Scan(
			&item.Queue, &item.Key, &item.Status, &item.Priority,
			&item.Attempts, &item.MaxAttempts, &item.NotBefore,
			&item.LeaseExpires, &item.WorkerID, &item.ErrorMessage,
			&item.CreatedAt, &item.UpdatedAt, &item.ClaimedAt, &item.CompletedAt,
		)
		if err != nil {
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

	// Update concurrency counter.
	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = in_progress + $1 WHERE queue = $2
	`, len(items), queue)
	if err != nil {
		return nil, fmt.Errorf("update in_progress: %w", err)
	}

	// Record history for each claimed item.
	for _, item := range items {
		_, err = tx.Exec(ctx, `
			INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, attempt)
			VALUES ($1, $2, 'pending', 'claimed', $3, $4)
		`, item.Queue, item.Key, workerID, item.Attempts)
		if err != nil {
			return nil, fmt.Errorf("record history: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return items, nil
}

func (c *Client) Complete(ctx context.Context, queue, key string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET status = 'succeeded',
			completed_at = now(),
			updated_at = now(),
			lease_expires = NULL
		WHERE queue = $1 AND key = $2
		  AND status IN ('claimed', 'running')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("complete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workqueue.ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = GREATEST(in_progress - 1, 0) WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("decrement in_progress: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		VALUES ($1, $2, 'running', 'succeeded')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) Fail(ctx context.Context, queue, key string, errMsg string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET status = 'failed',
			error_message = $3,
			completed_at = now(),
			updated_at = now(),
			lease_expires = NULL
		WHERE queue = $1 AND key = $2
		  AND status IN ('claimed', 'running')
	`, queue, key, errMsg)
	if err != nil {
		return fmt.Errorf("fail: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workqueue.ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = GREATEST(in_progress - 1, 0) WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("decrement in_progress: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, error_message)
		VALUES ($1, $2, 'running', 'failed', $3)
	`, queue, key, errMsg)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) Requeue(ctx context.Context, queue, key string, opts ...workqueue.RequeueOption) error {
	o := workqueue.ApplyRequeueOptions(opts)

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
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
	`, queue, key, o.NotBefore)
	if err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workqueue.ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = GREATEST(in_progress - 1, 0) WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("decrement in_progress: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		VALUES ($1, $2, 'running', 'pending')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) RequeueUndoAttempt(ctx context.Context, queue, key string, notBefore time.Time) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
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
	`, queue, key, notBefore)
	if err != nil {
		return fmt.Errorf("requeue undo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workqueue.ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = GREATEST(in_progress - 1, 0) WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("decrement in_progress: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		VALUES ($1, $2, 'claimed', 'pending')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) Deadletter(ctx context.Context, queue, key string) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE work_items
		SET status = 'dead_letter',
			completed_at = now(),
			updated_at = now(),
			lease_expires = NULL
		WHERE queue = $1 AND key = $2
		  AND status IN ('claimed', 'running', 'failed')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("deadletter: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return workqueue.ErrNotFound
	}

	_, err = tx.Exec(ctx, `
		UPDATE queue_state SET in_progress = GREATEST(in_progress - 1, 0) WHERE queue = $1
	`, queue)
	if err != nil {
		return fmt.Errorf("decrement in_progress: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status)
		VALUES ($1, $2, 'failed', 'dead_letter')
	`, queue, key)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) ExtendLease(ctx context.Context, queue, key string, duration time.Duration) error {
	tag, err := c.pool.Exec(ctx, `
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
		return workqueue.ErrNotFound
	}
	return nil
}

func (c *Client) Transition(ctx context.Context, queue, key string, from, to workqueue.Status, opts ...workqueue.TransitionOption) error {
	o := workqueue.ApplyTransitionOptions(opts)

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentStatus workqueue.Status
	err = tx.QueryRow(ctx, `
		SELECT status FROM work_items
		WHERE queue = $1 AND key = $2
		FOR UPDATE
	`, queue, key).Scan(&currentStatus)
	if err != nil {
		if err == pgx.ErrNoRows {
			return workqueue.ErrNotFound
		}
		return fmt.Errorf("read status: %w", err)
	}

	if currentStatus != from {
		return workqueue.ErrConflict
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

	_, err = tx.Exec(ctx, `
		INSERT INTO work_item_history (queue, key, from_status, to_status, worker_id, error_message)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''))
	`, queue, key, from, to, o.WorkerID, o.ErrorMessage)
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}

	return tx.Commit(ctx)
}

func (c *Client) CountByStatus(ctx context.Context, queue string) (map[workqueue.Status]int64, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT status, COUNT(*) FROM work_items
		WHERE queue = $1
		GROUP BY status
	`, queue)
	if err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[workqueue.Status]int64)
	for rows.Next() {
		var status workqueue.Status
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("scan count: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (c *Client) List(ctx context.Context, filter workqueue.ListFilter) ([]workqueue.WorkItem, error) {
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

	query += " ORDER BY priority DESC, created_at ASC"

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

	rows, err := c.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	var items []workqueue.WorkItem
	for rows.Next() {
		var item workqueue.WorkItem
		err := rows.Scan(
			&item.Queue, &item.Key, &item.Status, &item.Priority,
			&item.Attempts, &item.MaxAttempts, &item.NotBefore,
			&item.LeaseExpires, &item.WorkerID, &item.ErrorMessage,
			&item.CreatedAt, &item.UpdatedAt, &item.ClaimedAt, &item.CompletedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (c *Client) RepairCounter(ctx context.Context, queue string) error {
	_, err := c.pool.Exec(ctx, `
		UPDATE queue_state
		SET in_progress = (
			SELECT COUNT(*) FROM work_items
			WHERE queue = $1 AND status IN ('claimed', 'running')
		)
		WHERE queue = $1
	`, queue)
	return err
}

func (c *Client) EnsureQueue(ctx context.Context, queue string, cfg workqueue.QueueConfig) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO queue_state (queue, max_concurrency, max_retry, compute_backend)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (queue) DO NOTHING
	`, queue, cfg.MaxConcurrency, cfg.MaxRetry, cfg.ComputeBackend)
	return err
}
