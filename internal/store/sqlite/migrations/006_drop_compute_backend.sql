-- Remove deprecated compute_backend column from queue_state and worker_leases.
-- Scaling is now handled externally via KEDA/HPA.
--
-- SQLite does not support ALTER TABLE DROP COLUMN before version 3.35.0.
-- Recreate tables without the column to ensure compatibility.

-- Rebuild queue_state without compute_backend.
CREATE TABLE queue_state_new (
    queue           TEXT PRIMARY KEY,
    max_concurrency INTEGER NOT NULL DEFAULT 10,
    max_retry       INTEGER NOT NULL DEFAULT 5,
    in_progress     INTEGER NOT NULL DEFAULT 0,
    paused          INTEGER,
    leader_id       TEXT,
    leader_expires  TEXT
);

INSERT INTO queue_state_new (queue, max_concurrency, max_retry, in_progress, paused, leader_id, leader_expires)
SELECT queue, max_concurrency, max_retry, in_progress, paused, leader_id, leader_expires
FROM queue_state;

DROP TABLE queue_state;
ALTER TABLE queue_state_new RENAME TO queue_state;

-- Rebuild worker_leases without compute_backend.
CREATE TABLE worker_leases_new (
    worker_id       TEXT PRIMARY KEY,
    queue           TEXT NOT NULL,
    hostname        TEXT,
    started_at      TEXT NOT NULL,
    last_heartbeat  TEXT NOT NULL,
    items_processed INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'active'
);

INSERT INTO worker_leases_new (worker_id, queue, hostname, started_at, last_heartbeat, items_processed, status)
SELECT worker_id, queue, hostname, started_at, last_heartbeat, items_processed, status
FROM worker_leases;

DROP TABLE worker_leases;
ALTER TABLE worker_leases_new RENAME TO worker_leases;
