# Factory Core - Local Development Stack

Docker Compose stack for local development and integration testing.

## Architecture

```
factory-api (8080) → sf-pipeline queue → factory-orchestrator (8090)
                                       ↓
                       sf-stage queue → factory-sandbox-manager (8091)
                                       ↓
                       sf-output queue (Phase 2)
```

## Services

| Service | Port | Description |
|---------|------|-------------|
| postgres | 5432 | PostgreSQL database (workqueue + factory schemas) |
| factory-api | 8080 | HTTP API entrypoint, outbox poller |
| sf-pipeline-receiver | 8081 | Workqueue receiver for pipeline orchestration |
| sf-pipeline-dispatcher | - | Dispatches pipeline runs to orchestrator |
| factory-orchestrator | 8090 | Pipeline reconciler (DAG evaluation) |
| sf-stage-receiver | 8082 | Workqueue receiver for stage execution |
| sf-stage-dispatcher | - | Dispatches stages to sandbox manager |
| factory-sandbox-manager | 8091 | Sandbox lifecycle reconciler (Docker provider) |
| sf-output-receiver | 8083 | Workqueue receiver for output processing (Phase 2) |

## Usage

### Start the stack

```bash
cd deploy
docker-compose -f docker-compose.dev.yaml up --build
```

### Create a pipeline run

```bash
curl -X POST http://localhost:8080/api/v1/runs \
  -H "Content-Type: application/json" \
  -d '{
    "pipeline_repo": "github.com/test/test",
    "pipeline_path": "simple-test/.factory/test",
    "pipeline_ref": "main",
    "parameters": {
      "resource.test-repo.url": "github.com/test/repo"
    },
    "priority": 5
  }'
```

### Check run status

```bash
# List runs
curl http://localhost:8080/api/v1/runs

# Get specific run
curl http://localhost:8080/api/v1/runs/{run-id}

# List stages
curl http://localhost:8080/api/v1/runs/{run-id}/stages

# Audit events
curl http://localhost:8080/api/v1/runs/{run-id}/events
```

### Tail logs

```bash
# All services
docker-compose -f docker-compose.dev.yaml logs -f

# Specific service
docker-compose -f docker-compose.dev.yaml logs -f factory-api
docker-compose -f docker-compose.dev.yaml logs -f factory-orchestrator
docker-compose -f docker-compose.dev.yaml logs -f factory-sandbox-manager
```

### Stop the stack

```bash
docker-compose -f docker-compose.dev.yaml down

# With volume cleanup
docker-compose -f docker-compose.dev.yaml down -v
```

## Database Access

```bash
# Connect to PostgreSQL
docker-compose -f docker-compose.dev.yaml exec postgres psql -U factory

# Check factory schema
\c factory
\dt factory.*

# Query runs
SELECT id, phase, pipeline_path, created_at FROM factory.pipeline_runs;

# Query stages
SELECT id, run_id, stage_name, phase FROM factory.stage_runs;

# Check outbox
SELECT * FROM factory.outbox WHERE NOT sent;
```

## Workqueue Inspection

```bash
# Check work items
SELECT queue, key, status, priority, attempts FROM work_items;

# Check sf-pipeline queue
SELECT * FROM work_items WHERE queue = 'sf-pipeline';

# Check sf-stage queue
SELECT * FROM work_items WHERE queue = 'sf-stage';
```

## Development Notes

- Pipeline definitions loaded from `../examples/` (mounted read-only)
- Sandbox provider: Docker (requires Docker socket mount)
- Credentials: dev-only (factory/factory)
- Schema initialization: `init-db.sql` runs on first postgres startup
- Workqueue migrations: handled by workqueue services automatically

## Troubleshooting

**Postgres permission errors (rootless Docker):**

If you see `Operation not permitted` when postgres tries to initialize:
```
initdb: error: could not change permissions of directory "/var/lib/postgresql/data"
```

This is a known rootless Docker volume permission issue. Workarounds:

1. Use host PostgreSQL instead of containerized:
   ```bash
   # Install postgres locally
   sudo dnf install postgresql-server
   sudo postgresql-setup --initdb
   sudo systemctl start postgresql
   
   # Update DATABASE_URL in docker-compose to point to host
   # DATABASE_URL: postgres://factory:factory@host.docker.internal:5432/factory
   ```

2. Or run Docker with root privileges (not recommended for dev)

**Postgres fails to start:**
```bash
# Remove volume and restart
docker compose -f docker-compose.dev.yaml down -v
docker compose -f docker-compose.dev.yaml up postgres
```

**Build failures:**
```bash
# Clean rebuild
docker-compose -f docker-compose.dev.yaml build --no-cache
```

**No pipeline definitions found:**
```bash
# Check examples directory is mounted
docker-compose -f docker-compose.dev.yaml exec factory-api ls -la /pipelines
```

**Sandbox manager can't provision:**
```bash
# Check Docker socket mount
docker-compose -f docker-compose.dev.yaml exec factory-sandbox-manager ls -la /var/run/docker.sock

# Check Docker daemon accessible
docker-compose -f docker-compose.dev.yaml exec factory-sandbox-manager docker ps
```
