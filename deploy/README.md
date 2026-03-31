# Deployment

Docker Compose files for running the factory workqueue stack locally.

## Store backends

```bash
# PostgreSQL (recommended)
docker compose -f docker-compose.postgres.yaml up --build -d

# SQLite (single-node, no external database)
docker compose -f docker-compose.sqlite.yaml up --build -d

# DynamoDB + S3 (uses DynamoDB Local + rustfs)
docker compose -f docker-compose.dynamodb.yaml up --build -d
```

## Authorization

```bash
# Cedar (in-process policy evaluation)
docker compose -f docker-compose.cedar.yaml up --build -d

# OPA (external policy server)
docker compose -f docker-compose.opa.yaml up --build -d
```

## Observability

```bash
# Tracing with Jaeger (UI at http://localhost:16686)
docker compose -f docker-compose.tracing.yaml up --build -d
```

## Stress testing

```bash
# Max throughput (concurrency=100, delay=0)
docker compose -f docker-compose.stress.yaml up --build -d
seq 1 10000 | xargs -P100 -I{} curl -sf -o /dev/null \
  -X POST http://localhost:8081/enqueue -d '{"key":"stress-{}","priority":0}'
```

## Usage

All compose files expose the same endpoints:

| Service | Port | Purpose |
|---------|------|---------|
| Receiver | 8081 | `POST /enqueue` |
| Admin API | 18080 | `GET /admin/queues`, items, workers, events |
| Jaeger UI | 16686 | Trace visualization (tracing compose only) |

```bash
# Enqueue work
curl -X POST http://localhost:8081/enqueue -d '{"key":"test","priority":10}'

# Check status
curl http://localhost:18080/admin/queues

# Tear down
docker compose -f <file>.yaml down -v
```

## Kubernetes

See [kubernetes/](kubernetes/) for production Kubernetes/OpenShift manifests.
