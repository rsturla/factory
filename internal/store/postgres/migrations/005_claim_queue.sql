-- Introduce claim_queue side-table to enable HOT updates on work_items.
--
-- Partial indexes with WHERE status = 'pending' (and similar) block HOT
-- even though status is only in the predicate, not a B-tree key column.
-- Every status transition must update these indexes, dropping HOT from
-- ~90% to 0% and causing write amplification.
--
-- claim_queue holds one row per pending item. ClaimBatch DELETEs from
-- claim_queue (SKIP LOCKED) then UPDATEs work_items — the UPDATE is HOT
-- because no index on work_items references status anymore.

CREATE TABLE claim_queue (
    queue       TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    priority    INT         NOT NULL DEFAULT 0,
    not_before  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (queue, key)
);

CREATE INDEX idx_claim_queue_dispatch
    ON claim_queue (queue, not_before NULLS FIRST, priority DESC, created_at ASC);

ALTER TABLE claim_queue SET (
    autovacuum_vacuum_scale_factor = 0.01,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_cost_delay = 1
);

-- Populate from existing pending items.
INSERT INTO claim_queue (queue, key, priority, not_before, created_at)
SELECT queue, key, priority, not_before, created_at
FROM work_items
WHERE status = 'pending';

-- Drop all partial indexes that reference status.
DROP INDEX IF EXISTS idx_work_items_claimable;
DROP INDEX IF EXISTS idx_work_items_lease_expires;
DROP INDEX IF EXISTS idx_work_items_completed_at;
