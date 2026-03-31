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
