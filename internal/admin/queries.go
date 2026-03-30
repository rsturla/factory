// Package admin provides query functions for the admin API that go beyond
// the core workqueue.Interface — queue listing, history lookups, worker listing.
package admin

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory/internal/workqueue"
)

// Queries provides read-only admin queries against the factory database.
type Queries struct {
	pool *pgxpool.Pool
}

// NewQueries creates a new admin Queries instance.
func NewQueries(pool *pgxpool.Pool) *Queries {
	return &Queries{pool: pool}
}

// QueueInfo describes a queue and its current state.
type QueueInfo struct {
	Name           string         `json:"name"`
	MaxConcurrency int            `json:"max_concurrency"`
	MaxRetry       int            `json:"max_retry"`
	ComputeBackend string         `json:"compute_backend"`
	InProgress     int            `json:"in_progress"`
	Counts         map[string]int `json:"counts"`
}

// ListQueues returns all registered queues with item counts.
func (q *Queries) ListQueues(ctx context.Context) ([]QueueInfo, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT qs.queue, qs.max_concurrency, qs.max_retry, qs.compute_backend, qs.in_progress
		FROM queue_state qs
		ORDER BY qs.queue
	`)
	if err != nil {
		return nil, fmt.Errorf("list queues: %w", err)
	}
	defer rows.Close()

	var queues []QueueInfo
	for rows.Next() {
		var qi QueueInfo
		if err := rows.Scan(&qi.Name, &qi.MaxConcurrency, &qi.MaxRetry, &qi.ComputeBackend, &qi.InProgress); err != nil {
			return nil, fmt.Errorf("scan queue: %w", err)
		}
		qi.Counts = make(map[string]int)
		queues = append(queues, qi)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill in counts per queue.
	for i := range queues {
		counts, err := q.pool.Query(ctx, `
			SELECT status, COUNT(*)::int FROM work_items
			WHERE queue = $1 GROUP BY status
		`, queues[i].Name)
		if err != nil {
			return nil, fmt.Errorf("count items for %s: %w", queues[i].Name, err)
		}
		for counts.Next() {
			var status string
			var count int
			if err := counts.Scan(&status, &count); err != nil {
				counts.Close()
				return nil, err
			}
			queues[i].Counts[status] = count
		}
		counts.Close()
	}

	return queues, nil
}

// HistoryEntry represents a single state transition.
type HistoryEntry struct {
	ID           int64      `json:"id"`
	Queue        string     `json:"queue"`
	Key          string     `json:"key"`
	FromStatus   *string    `json:"from_status"`
	ToStatus     string     `json:"to_status"`
	WorkerID     *string    `json:"worker_id,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	Attempt      *int       `json:"attempt,omitempty"`
	TraceID      *string    `json:"trace_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// GetItemHistory returns the state transition history for a work item.
func (q *Queries) GetItemHistory(ctx context.Context, queue, key string) ([]HistoryEntry, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT id, queue, key, from_status, to_status, worker_id,
			error_message, attempt, trace_id, created_at
		FROM work_item_history
		WHERE queue = $1 AND key = $2
		ORDER BY created_at DESC
		LIMIT 100
	`, queue, key)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer rows.Close()

	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.ID, &e.Queue, &e.Key, &e.FromStatus, &e.ToStatus,
			&e.WorkerID, &e.ErrorMessage, &e.Attempt, &e.TraceID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ItemDetail is a work item with its history attached.
type ItemDetail struct {
	Item    workqueue.WorkItem `json:"item"`
	History []HistoryEntry     `json:"history"`
}

// GetItem returns a single work item with its history.
func (q *Queries) GetItem(ctx context.Context, queue, key string) (*ItemDetail, error) {
	var item workqueue.WorkItem
	err := q.pool.QueryRow(ctx, `
		SELECT queue, key, status, priority, attempts, max_attempts,
			not_before, lease_expires, worker_id, error_message,
			created_at, updated_at, claimed_at, completed_at
		FROM work_items
		WHERE queue = $1 AND key = $2
	`, queue, key).Scan(
		&item.Queue, &item.Key, &item.Status, &item.Priority,
		&item.Attempts, &item.MaxAttempts, &item.NotBefore,
		&item.LeaseExpires, &item.WorkerID, &item.ErrorMessage,
		&item.CreatedAt, &item.UpdatedAt, &item.ClaimedAt, &item.CompletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}

	history, err := q.GetItemHistory(ctx, queue, key)
	if err != nil {
		return nil, err
	}

	return &ItemDetail{Item: item, History: history}, nil
}

// WorkerInfo describes a registered worker.
type WorkerInfo struct {
	WorkerID       string     `json:"worker_id"`
	Queue          string     `json:"queue"`
	ComputeBackend string     `json:"compute_backend"`
	Hostname       *string    `json:"hostname,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	LastHeartbeat  time.Time  `json:"last_heartbeat"`
	ItemsProcessed int64      `json:"items_processed"`
	Status         string     `json:"status"`
}

// ListWorkers returns all registered workers, optionally filtered by queue.
func (q *Queries) ListWorkers(ctx context.Context, queue string) ([]WorkerInfo, error) {
	query := `
		SELECT worker_id, queue, compute_backend, hostname,
			started_at, last_heartbeat, items_processed, status
		FROM worker_leases
	`
	var args []any
	if queue != "" {
		query += " WHERE queue = $1"
		args = append(args, queue)
	}
	query += " ORDER BY queue, worker_id"

	rows, err := q.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer rows.Close()

	var workers []WorkerInfo
	for rows.Next() {
		var w WorkerInfo
		if err := rows.Scan(&w.WorkerID, &w.Queue, &w.ComputeBackend, &w.Hostname,
			&w.StartedAt, &w.LastHeartbeat, &w.ItemsProcessed, &w.Status); err != nil {
			return nil, fmt.Errorf("scan worker: %w", err)
		}
		workers = append(workers, w)
	}
	return workers, rows.Err()
}

// PurgeDeadLetters deletes dead-lettered items for a queue.
// Returns the number of items purged.
func (q *Queries) PurgeDeadLetters(ctx context.Context, queue string) (int64, error) {
	tag, err := q.pool.Exec(ctx, `
		DELETE FROM work_items WHERE queue = $1 AND status = 'dead_letter'
	`, queue)
	if err != nil {
		return 0, fmt.Errorf("purge dead letters: %w", err)
	}
	return tag.RowsAffected(), nil
}
