# Database Schema Migration

## Schema Version: Phase 2.1 - Agent Execution Tracking

### Changes

Added fields to `factory.stage_runs`:
- `agent_exec_id TEXT` - Provider-specific agent process ID for status polling
- `output_config JSONB` - Stage output configuration

### Migration Steps (Development)

For local docker-compose development:

```bash
# Stop all services
docker-compose -f deploy/docker-compose.dev.yaml down

# Remove postgres volume (WARNING: deletes all data)
docker volume rm deploy_pgdata

# Restart services (init-db.sql will create new schema)
docker-compose -f deploy/docker-compose.dev.yaml up -d
```

### Migration Steps (Production)

For production PostgreSQL:

```sql
-- Add new columns
ALTER TABLE factory.stage_runs 
  ADD COLUMN IF NOT EXISTS agent_exec_id TEXT,
  ADD COLUMN IF NOT EXISTS output_config JSONB;
```

No data migration needed - new columns nullable, existing rows get NULL.

### Rollback

```sql
ALTER TABLE factory.stage_runs 
  DROP COLUMN IF EXISTS agent_exec_id,
  DROP COLUMN IF EXISTS output_config;
```

### Version Compatibility

- Backward compatible: Old code can read new schema (ignores new columns)
- Forward incompatible: New code requires new columns (will fail on UpdateStage)
