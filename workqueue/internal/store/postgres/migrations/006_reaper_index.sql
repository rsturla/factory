-- Replace status-referencing indexes with a side-table for lease tracking.
-- This preserves full HOT update capability on work_items — no index on
-- work_items references the status column.

DROP INDEX IF EXISTS idx_work_items_reaper;
DROP INDEX IF EXISTS idx_work_items_completed_at;

CREATE TABLE IF NOT EXISTS active_leases (
    queue         TEXT        NOT NULL,
    key           TEXT        NOT NULL,
    worker_id     TEXT        NOT NULL DEFAULT '',
    lease_expires TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (queue, key)
);

CREATE INDEX IF NOT EXISTS idx_active_leases_expiry
    ON active_leases (queue, lease_expires);

-- Populate from existing in-flight items.
INSERT INTO active_leases (queue, key, worker_id, lease_expires)
SELECT queue, key, COALESCE(worker_id, ''), lease_expires
FROM work_items
WHERE status IN ('claimed', 'running')
  AND lease_expires IS NOT NULL
ON CONFLICT DO NOTHING;
