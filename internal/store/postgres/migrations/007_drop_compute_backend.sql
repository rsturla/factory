-- Remove deprecated compute_backend column from queue_state and worker_leases.
-- Scaling is now handled externally via KEDA/HPA.

ALTER TABLE queue_state DROP COLUMN IF EXISTS compute_backend;
ALTER TABLE worker_leases DROP COLUMN IF EXISTS compute_backend;
