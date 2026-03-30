// Package history records state transitions for work items into the
// work_item_history table for audit and debugging.
package history

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Recorder writes work item state transitions to the history table.
type Recorder struct {
	pool *pgxpool.Pool
}

// New creates a new history Recorder.
func New(pool *pgxpool.Pool) *Recorder {
	return &Recorder{pool: pool}
}

// Record writes a single state transition to the history table.
func (r *Recorder) Record(ctx context.Context, entry Entry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO work_item_history
			(queue, key, from_status, to_status, worker_id, error_message, attempt, trace_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, entry.Queue, entry.Key, entry.FromStatus, entry.ToStatus,
		nilIfEmpty(entry.WorkerID), nilIfEmpty(entry.ErrorMessage),
		entry.Attempt, nilIfEmpty(entry.TraceID))
	if err != nil {
		return fmt.Errorf("record history: %w", err)
	}
	return nil
}

// RecordBatch writes multiple state transitions in a single round-trip.
func (r *Recorder) RecordBatch(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}

	batch := &pgxBatch{}
	for _, e := range entries {
		batch.queue(
			`INSERT INTO work_item_history
				(queue, key, from_status, to_status, worker_id, error_message, attempt, trace_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			e.Queue, e.Key, e.FromStatus, e.ToStatus,
			nilIfEmpty(e.WorkerID), nilIfEmpty(e.ErrorMessage),
			e.Attempt, nilIfEmpty(e.TraceID),
		)
	}

	return batch.exec(ctx, r.pool)
}

// Entry represents a single state transition record.
type Entry struct {
	Queue        string
	Key          string
	FromStatus   string
	ToStatus     string
	WorkerID     string
	ErrorMessage string
	Attempt      int
	TraceID      string
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pgxBatch collects queries for batch execution.
type pgxBatch struct {
	queries []batchQuery
}

type batchQuery struct {
	sql  string
	args []any
}

func (b *pgxBatch) queue(sql string, args ...any) {
	b.queries = append(b.queries, batchQuery{sql: sql, args: args})
}

func (b *pgxBatch) exec(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin batch tx: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, q := range b.queries {
		if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
			return fmt.Errorf("batch exec: %w", err)
		}
	}

	return tx.Commit(ctx)
}
