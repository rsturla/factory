-- Performance tuning for high-throughput queue workloads.
--
-- fillfactor=70 reserves 30% of each heap page for in-place HOT updates.
-- Full HOT enablement requires migration 005 which drops all partial
-- indexes that reference status. This migration prepares by dropping
-- the unnecessary (queue, status) index and tuning autovacuum.
--
-- Aggressive autovacuum keeps indexes tight. Queue tables churn dead
-- tuples at a rate proportional to throughput — default thresholds
-- (20% scale factor) let bloat accumulate too long.

DROP INDEX IF EXISTS idx_work_items_queue_status;

ALTER TABLE work_items SET (fillfactor = 70);
ALTER TABLE work_items SET (
    autovacuum_vacuum_scale_factor = 0.01,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_cost_delay = 1
);

ALTER TABLE work_item_history SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.05
);
