-- Factory V2 Work Queue Schema
-- This schema is owned by the factory platform.
-- Domain-specific tables belong in reconciler repos.

CREATE TABLE IF NOT EXISTS work_items (
    queue           TEXT        NOT NULL,
    key             TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                    CHECK (status IN (
                        'pending','claimed','running',
                        'succeeded','failed','dead_letter'
                    )),
    priority        INT         NOT NULL DEFAULT 0,
    attempts        INT         NOT NULL DEFAULT 0,
    max_attempts    INT         NOT NULL DEFAULT 5,
    not_before      TIMESTAMPTZ,
    lease_expires   TIMESTAMPTZ,
    worker_id       TEXT,
    error_message   TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,

    PRIMARY KEY (queue, key)
);

-- Claim query: pending items ordered by priority (per queue)
CREATE INDEX IF NOT EXISTS idx_work_items_claimable
    ON work_items (queue, priority DESC, created_at ASC)
    WHERE status = 'pending';

-- Reaper: expired leases
CREATE INDEX IF NOT EXISTS idx_work_items_lease_expires
    ON work_items (lease_expires)
    WHERE status IN ('claimed', 'running')
      AND lease_expires IS NOT NULL;

-- Metrics/admin: counts by status
CREATE INDEX IF NOT EXISTS idx_work_items_queue_status
    ON work_items (queue, status);


CREATE TABLE IF NOT EXISTS work_item_history (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    queue           TEXT        NOT NULL,
    key             TEXT        NOT NULL,
    from_status     TEXT,
    to_status       TEXT        NOT NULL,
    worker_id       TEXT,
    error_message   TEXT,
    attempt         INT,
    trace_id        TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_history_queue_key
    ON work_item_history (queue, key, created_at DESC);


CREATE TABLE IF NOT EXISTS worker_leases (
    worker_id       TEXT        PRIMARY KEY,
    queue           TEXT        NOT NULL,
    compute_backend TEXT        NOT NULL,
    hostname        TEXT,
    metadata        JSONB,
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat  TIMESTAMPTZ NOT NULL DEFAULT now(),
    items_processed BIGINT      NOT NULL DEFAULT 0,
    status          TEXT        NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'draining', 'stopped'))
);

CREATE INDEX IF NOT EXISTS idx_worker_leases_queue
    ON worker_leases (queue, status);

CREATE INDEX IF NOT EXISTS idx_worker_leases_heartbeat
    ON worker_leases (last_heartbeat)
    WHERE status = 'active';


CREATE TABLE IF NOT EXISTS queue_state (
    queue           TEXT        PRIMARY KEY,
    max_concurrency INT         NOT NULL DEFAULT 10,
    max_retry       INT         NOT NULL DEFAULT 5,
    compute_backend TEXT        NOT NULL DEFAULT 'kubernetes',
    leader_id       TEXT,
    leader_expires  TIMESTAMPTZ,
    last_reconcile  TIMESTAMPTZ,
    in_progress     INT         NOT NULL DEFAULT 0,
    config          JSONB       NOT NULL DEFAULT '{}'
);


-- Real-time notifications via PG LISTEN/NOTIFY
CREATE OR REPLACE FUNCTION notify_work_item_change() RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('work_item_' || NEW.queue,
        json_build_object(
            'key', NEW.key,
            'status', NEW.status,
            'priority', NEW.priority
        )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS work_item_notify ON work_items;
CREATE TRIGGER work_item_notify
    AFTER INSERT OR UPDATE OF status ON work_items
    FOR EACH ROW EXECUTE FUNCTION notify_work_item_change();
