-- Factory Core Database Schema
-- Development/testing initialization script

CREATE SCHEMA IF NOT EXISTS factory;

CREATE TABLE IF NOT EXISTS factory.pipeline_runs (
    id              TEXT PRIMARY KEY,
    phase           TEXT NOT NULL,
    pipeline_repo   TEXT NOT NULL,
    pipeline_path   TEXT NOT NULL,
    pipeline_commit TEXT NOT NULL,
    pipeline_spec   JSONB NOT NULL,
    parameters      JSONB NOT NULL,
    resource_bindings JSONB NOT NULL,
    priority        INT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_pipeline_runs_phase ON factory.pipeline_runs(phase);
CREATE INDEX IF NOT EXISTS idx_pipeline_runs_created_at ON factory.pipeline_runs(created_at DESC);

CREATE TABLE IF NOT EXISTS factory.stage_runs (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES factory.pipeline_runs(id) ON DELETE CASCADE,
    stage_name      TEXT NOT NULL,
    phase           TEXT NOT NULL,
    sandbox_id      TEXT,
    agent_exec_id   TEXT,
    agent_config    JSONB NOT NULL,
    output_config   JSONB,
    output          JSONB,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_stage_runs_run_id ON factory.stage_runs(run_id);
CREATE INDEX IF NOT EXISTS idx_stage_runs_run_id_stage_name ON factory.stage_runs(run_id, stage_name);

CREATE TABLE IF NOT EXISTS factory.audit_events (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL,
    stage_id        TEXT,
    event_type      TEXT NOT NULL,
    detail          JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_events_run_id ON factory.audit_events(run_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON factory.audit_events(created_at DESC);

CREATE TABLE IF NOT EXISTS factory.outbox (
    id              BIGSERIAL PRIMARY KEY,
    queue           TEXT NOT NULL,
    key             TEXT NOT NULL,
    priority        INT NOT NULL,
    sent            BOOLEAN DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_outbox_sent_created ON factory.outbox(sent, created_at) WHERE NOT sent;
