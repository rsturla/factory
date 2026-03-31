-- Index for completed item cleanup job.
-- Enables efficient deletion of old succeeded/failed items.
CREATE INDEX IF NOT EXISTS idx_work_items_completed_at
    ON work_items (completed_at)
    WHERE status IN ('succeeded', 'failed')
      AND completed_at IS NOT NULL;
