package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
)

// Store implements runstore.Store using PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// New creates a PostgreSQL-backed run store.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateRun inserts a new pipeline run.
func (s *Store) CreateRun(ctx context.Context, run *v1.PipelineRun) error {
	specJSON, err := json.Marshal(run.PipelineSpec)
	if err != nil {
		return fmt.Errorf("marshal pipeline spec: %w", err)
	}
	paramsJSON, err := json.Marshal(run.Parameters)
	if err != nil {
		return fmt.Errorf("marshal parameters: %w", err)
	}
	bindingsJSON, err := json.Marshal(run.ResourceBindings)
	if err != nil {
		return fmt.Errorf("marshal resource bindings: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO factory.pipeline_runs (
			id, phase, pipeline_repo, pipeline_path, pipeline_commit,
			pipeline_spec, parameters, resource_bindings, priority,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, run.ID, run.Phase, run.PipelineRepo, run.PipelinePath, run.PipelineCommit,
		specJSON, paramsJSON, bindingsJSON, run.Priority, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// CreateRunWithOutbox atomically creates run and outbox entry in single transaction.
func (s *Store) CreateRunWithOutbox(ctx context.Context, run *v1.PipelineRun, outbox runstore.OutboxEntry) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Marshal JSONs
	specJSON, err := json.Marshal(run.PipelineSpec)
	if err != nil {
		return fmt.Errorf("marshal pipeline spec: %w", err)
	}
	paramsJSON, err := json.Marshal(run.Parameters)
	if err != nil {
		return fmt.Errorf("marshal parameters: %w", err)
	}
	bindingsJSON, err := json.Marshal(run.ResourceBindings)
	if err != nil {
		return fmt.Errorf("marshal resource bindings: %w", err)
	}

	// Insert run
	_, err = tx.Exec(ctx, `
		INSERT INTO factory.pipeline_runs (
			id, phase, pipeline_repo, pipeline_path, pipeline_commit,
			pipeline_spec, parameters, resource_bindings, priority,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, run.ID, run.Phase, run.PipelineRepo, run.PipelinePath, run.PipelineCommit,
		specJSON, paramsJSON, bindingsJSON, run.Priority, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert run: %w", err)
	}

	// Insert outbox entry
	_, err = tx.Exec(ctx, `
		INSERT INTO factory.outbox (queue, key, priority, created_at)
		VALUES ($1, $2, $3, $4)
	`, outbox.Queue, outbox.Key, outbox.Priority, outbox.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert outbox: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// GetRun retrieves a pipeline run by ID.
func (s *Store) GetRun(ctx context.Context, id string) (*v1.PipelineRun, error) {
	var run v1.PipelineRun
	var specJSON, paramsJSON, bindingsJSON []byte
	var completedAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT id, phase, pipeline_repo, pipeline_path, pipeline_commit,
		       pipeline_spec, parameters, resource_bindings, priority,
		       created_at, updated_at, completed_at
		FROM factory.pipeline_runs WHERE id = $1
	`, id).Scan(
		&run.ID, &run.Phase, &run.PipelineRepo, &run.PipelinePath, &run.PipelineCommit,
		&specJSON, &paramsJSON, &bindingsJSON, &run.Priority,
		&run.CreatedAt, &run.UpdatedAt, &completedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("run not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("query run: %w", err)
	}

	run.CompletedAt = completedAt

	if err := json.Unmarshal(specJSON, &run.PipelineSpec); err != nil {
		return nil, fmt.Errorf("unmarshal spec: %w", err)
	}
	if err := json.Unmarshal(paramsJSON, &run.Parameters); err != nil {
		return nil, fmt.Errorf("unmarshal params: %w", err)
	}
	if err := json.Unmarshal(bindingsJSON, &run.ResourceBindings); err != nil {
		return nil, fmt.Errorf("unmarshal bindings: %w", err)
	}

	return &run, nil
}

// UpdateRun updates an existing pipeline run.
func (s *Store) UpdateRun(ctx context.Context, run *v1.PipelineRun) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE factory.pipeline_runs
		SET phase = $2, updated_at = $3, completed_at = $4
		WHERE id = $1
	`, run.ID, run.Phase, run.UpdatedAt, run.CompletedAt)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	return nil
}

// ListRuns retrieves pipeline runs with filtering.
func (s *Store) ListRuns(ctx context.Context, filters runstore.ListRunsFilters) ([]v1.PipelineRun, string, error) {
	limit := filters.Limit
	if limit == 0 {
		limit = 50
	}

	query := `
		SELECT id, phase, pipeline_repo, pipeline_path, pipeline_commit,
		       pipeline_spec, parameters, resource_bindings, priority,
		       created_at, updated_at, completed_at
		FROM factory.pipeline_runs
	`
	args := []any{}
	argIdx := 1

	if filters.Phase != "" {
		query += fmt.Sprintf(" WHERE phase = $%d", argIdx)
		args = append(args, filters.Phase)
		argIdx++
	}

	query += " ORDER BY created_at DESC"
	query += fmt.Sprintf(" LIMIT $%d", argIdx)
	args = append(args, limit+1) // fetch one extra to detect next page

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()

	runs := []v1.PipelineRun{}
	for rows.Next() {
		var run v1.PipelineRun
		var specJSON, paramsJSON, bindingsJSON []byte
		var completedAt *time.Time

		if err := rows.Scan(
			&run.ID, &run.Phase, &run.PipelineRepo, &run.PipelinePath, &run.PipelineCommit,
			&specJSON, &paramsJSON, &bindingsJSON, &run.Priority,
			&run.CreatedAt, &run.UpdatedAt, &completedAt,
		); err != nil {
			return nil, "", fmt.Errorf("scan run: %w", err)
		}

		run.CompletedAt = completedAt

		if err := json.Unmarshal(specJSON, &run.PipelineSpec); err != nil {
			return nil, "", fmt.Errorf("unmarshal spec: %w", err)
		}
		if err := json.Unmarshal(paramsJSON, &run.Parameters); err != nil {
			return nil, "", fmt.Errorf("unmarshal params: %w", err)
		}
		if err := json.Unmarshal(bindingsJSON, &run.ResourceBindings); err != nil {
			return nil, "", fmt.Errorf("unmarshal bindings: %w", err)
		}

		runs = append(runs, run)
	}

	nextToken := ""
	if len(runs) > limit {
		runs = runs[:limit]
		nextToken = runs[limit-1].ID // use last ID as pagination token
	}

	return runs, nextToken, nil
}

// CreateStage inserts a new stage run.
func (s *Store) CreateStage(ctx context.Context, stage *v1.StageRun) error {
	agentJSON, err := json.Marshal(stage.AgentConfig)
	if err != nil {
		return fmt.Errorf("marshal agent config: %w", err)
	}
	outputConfigJSON, err := json.Marshal(stage.OutputConfig)
	if err != nil {
		return fmt.Errorf("marshal output config: %w", err)
	}
	outputJSON, err := json.Marshal(stage.Output)
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO factory.stage_runs (
			id, run_id, stage_name, phase, sandbox_id, agent_exec_id,
			agent_config, output_config, output, initial_git_commit, started_at, completed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, stage.ID, stage.RunID, stage.StageName, stage.Phase, stage.SandboxID, stage.AgentExecID,
		agentJSON, outputConfigJSON, outputJSON, stage.InitialGitCommit, stage.StartedAt, stage.CompletedAt)
	if err != nil {
		return fmt.Errorf("insert stage: %w", err)
	}
	return nil
}

// GetStage retrieves a stage run by ID.
func (s *Store) GetStage(ctx context.Context, id string) (*v1.StageRun, error) {
	var stage v1.StageRun
	var agentJSON, outputConfigJSON, outputJSON []byte
	var sandboxID, agentExecID, initialGitCommit *string
	var startedAt, completedAt *time.Time

	err := s.pool.QueryRow(ctx, `
		SELECT id, run_id, stage_name, phase, sandbox_id, agent_exec_id,
		       agent_config, output_config, output, initial_git_commit, started_at, completed_at
		FROM factory.stage_runs WHERE id = $1
	`, id).Scan(
		&stage.ID, &stage.RunID, &stage.StageName, &stage.Phase, &sandboxID, &agentExecID,
		&agentJSON, &outputConfigJSON, &outputJSON, &initialGitCommit, &startedAt, &completedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, fmt.Errorf("stage not found: %s", id)
	}
	if err != nil {
		return nil, fmt.Errorf("query stage: %w", err)
	}

	if sandboxID != nil {
		stage.SandboxID = *sandboxID
	}
	if agentExecID != nil {
		stage.AgentExecID = *agentExecID
	}
	if initialGitCommit != nil {
		stage.InitialGitCommit = *initialGitCommit
	}
	stage.StartedAt = startedAt
	stage.CompletedAt = completedAt

	if err := json.Unmarshal(agentJSON, &stage.AgentConfig); err != nil {
		return nil, fmt.Errorf("unmarshal agent config: %w", err)
	}
	if len(outputConfigJSON) > 0 {
		if err := json.Unmarshal(outputConfigJSON, &stage.OutputConfig); err != nil {
			return nil, fmt.Errorf("unmarshal output config: %w", err)
		}
	}
	if len(outputJSON) > 0 {
		if err := json.Unmarshal(outputJSON, &stage.Output); err != nil {
			return nil, fmt.Errorf("unmarshal output: %w", err)
		}
	}

	return &stage, nil
}

// UpdateStage updates an existing stage run.
func (s *Store) UpdateStage(ctx context.Context, stage *v1.StageRun) error {
	outputJSON, err := json.Marshal(stage.Output)
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE factory.stage_runs
		SET phase = $2, sandbox_id = $3, agent_exec_id = $4, output = $5, initial_git_commit = $6, started_at = $7, completed_at = $8
		WHERE id = $1
	`, stage.ID, stage.Phase, stage.SandboxID, stage.AgentExecID, outputJSON, stage.InitialGitCommit, stage.StartedAt, stage.CompletedAt)
	if err != nil {
		return fmt.Errorf("update stage: %w", err)
	}
	return nil
}

// ListStages retrieves all stages for a run.
func (s *Store) ListStages(ctx context.Context, runID string) ([]v1.StageRun, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, stage_name, phase, sandbox_id, agent_exec_id,
		       agent_config, output_config, output, initial_git_commit, started_at, completed_at
		FROM factory.stage_runs WHERE run_id = $1
		ORDER BY stage_name
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query stages: %w", err)
	}
	defer rows.Close()

	stages := []v1.StageRun{}
	for rows.Next() {
		var stage v1.StageRun
		var agentJSON, outputConfigJSON, outputJSON []byte
		var sandboxID, agentExecID, initialGitCommit *string
		var startedAt, completedAt *time.Time

		if err := rows.Scan(
			&stage.ID, &stage.RunID, &stage.StageName, &stage.Phase, &sandboxID, &agentExecID,
			&agentJSON, &outputConfigJSON, &outputJSON, &initialGitCommit, &startedAt, &completedAt,
		); err != nil {
			return nil, fmt.Errorf("scan stage: %w", err)
		}

		if sandboxID != nil {
			stage.SandboxID = *sandboxID
		}
		if agentExecID != nil {
			stage.AgentExecID = *agentExecID
		}
		if initialGitCommit != nil {
			stage.InitialGitCommit = *initialGitCommit
		}
		stage.StartedAt = startedAt
		stage.CompletedAt = completedAt

		if err := json.Unmarshal(agentJSON, &stage.AgentConfig); err != nil {
			return nil, fmt.Errorf("unmarshal agent config: %w", err)
		}
		if len(outputConfigJSON) > 0 {
			if err := json.Unmarshal(outputConfigJSON, &stage.OutputConfig); err != nil {
				return nil, fmt.Errorf("unmarshal output config: %w", err)
			}
		}
		if len(outputJSON) > 0 {
			if err := json.Unmarshal(outputJSON, &stage.Output); err != nil {
				return nil, fmt.Errorf("unmarshal output: %w", err)
			}
		}

		stages = append(stages, stage)
	}

	return stages, nil
}

// CreateAuditEvent inserts an audit event.
func (s *Store) CreateAuditEvent(ctx context.Context, event *v1.AuditEvent) error {
	detailJSON, err := json.Marshal(event.Detail)
	if err != nil {
		return fmt.Errorf("marshal detail: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO factory.audit_events (id, run_id, stage_id, event_type, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, event.ID, event.RunID, event.StageID, event.EventType, detailJSON, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// ListAuditEvents retrieves all audit events for a run.
func (s *Store) ListAuditEvents(ctx context.Context, runID string) ([]v1.AuditEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, run_id, stage_id, event_type, detail, created_at
		FROM factory.audit_events WHERE run_id = $1
		ORDER BY created_at
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	defer rows.Close()

	events := []v1.AuditEvent{}
	for rows.Next() {
		var event v1.AuditEvent
		var detailJSON []byte
		var stageID *string

		if err := rows.Scan(
			&event.ID, &event.RunID, &stageID, &event.EventType, &detailJSON, &event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}

		if stageID != nil {
			event.StageID = *stageID
		}

		if err := json.Unmarshal(detailJSON, &event.Detail); err != nil {
			return nil, fmt.Errorf("unmarshal detail: %w", err)
		}

		events = append(events, event)
	}

	return events, nil
}

// OutboxEnqueue adds an entry to the outbox.
func (s *Store) OutboxEnqueue(ctx context.Context, entry runstore.OutboxEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO factory.outbox (queue, key, priority, created_at)
		VALUES ($1, $2, $3, $4)
	`, entry.Queue, entry.Key, entry.Priority, entry.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert outbox entry: %w", err)
	}
	return nil
}

// OutboxPoll retrieves unsent outbox entries with row-level locking.
func (s *Store) OutboxPoll(ctx context.Context, limit int) ([]runstore.OutboxEntry, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, queue, key, priority, sent, created_at
		FROM factory.outbox
		WHERE sent = false
		ORDER BY created_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query outbox: %w", err)
	}
	defer rows.Close()

	entries := []runstore.OutboxEntry{}
	for rows.Next() {
		var entry runstore.OutboxEntry
		if err := rows.Scan(
			&entry.ID, &entry.Queue, &entry.Key, &entry.Priority, &entry.Sent, &entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan outbox entry: %w", err)
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

// OutboxMarkSent marks outbox entries as sent.
func (s *Store) OutboxMarkSent(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	_, err := s.pool.Exec(ctx, `
		UPDATE factory.outbox SET sent = true WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return fmt.Errorf("mark outbox sent: %w", err)
	}
	return nil
}
