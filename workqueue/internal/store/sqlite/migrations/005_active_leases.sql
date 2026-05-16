CREATE TABLE IF NOT EXISTS active_leases (
    queue TEXT NOT NULL,
    key TEXT NOT NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    lease_expires TEXT NOT NULL,
    PRIMARY KEY (queue, key)
);

CREATE INDEX IF NOT EXISTS idx_active_leases_expiry ON active_leases (queue, lease_expires);

-- Populate from existing in-flight items.
INSERT INTO active_leases (queue, key, worker_id, lease_expires)
SELECT queue, key, COALESCE(worker_id, ''), lease_expires
FROM work_items
WHERE status IN ('claimed', 'running')
  AND lease_expires IS NOT NULL
ON CONFLICT DO NOTHING;
