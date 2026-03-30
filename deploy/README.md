# Deployment

Docker Compose files for running the factory stack locally with each store backend.

## Usage

All commands are run from the `deploy/` directory.

### PostgreSQL (recommended for production)

```bash
docker compose -f docker-compose.postgres.yaml up --build -d

# Enqueue work
curl -X POST http://localhost:8081/enqueue -d '{"key":"curl.fc43","priority":10}'

# Check status
curl http://localhost:18080/admin/queues/echo

# Watch reconciler
docker compose -f docker-compose.postgres.yaml logs echo-reconciler -f

# Tear down
docker compose -f docker-compose.postgres.yaml down -v
```

### SQLite (single-node, no external database)

```bash
docker compose -f docker-compose.sqlite.yaml up --build -d

curl -X POST http://localhost:8081/enqueue -d '{"key":"test-pkg","priority":5}'

docker compose -f docker-compose.sqlite.yaml down -v
```

### DynamoDB + S3 (AWS-native, uses DynamoDB Local + rustfs)

```bash
docker compose -f docker-compose.dynamodb.yaml up --build -d

curl -X POST http://localhost:8081/enqueue -d '{"key":"test-pkg","priority":5}'

docker compose -f docker-compose.dynamodb.yaml down -v
```

## Services

Each compose file runs the same logical stack:

| Service | Port | Purpose |
|---------|------|---------|
| echo-receiver | 8081 | Accepts `/enqueue` and `/webhook` requests |
| echo-dispatcher | 8083 | Claims items, calls reconciler |
| echo-reconciler | (internal) | Logs the key, sleeps 2s, returns completed |
| admin | 18080 | Cross-queue admin API (postgres only) |

## Store backends

All three binaries (receiver, dispatcher, admin) support `STORE_BACKEND` env var:

| Value | Required env vars |
|-------|-------------------|
| `postgres` (default) | `DATABASE_URL` |
| `dynamodb` | `DDB_TABLE`, `S3_BUCKET`, `AWS_REGION` |
| `sqlite` | `SQLITE_PATH` |
