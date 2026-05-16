# Factory Workqueue

A pure workqueue platform for orchestrating software factory operations at scale. Designed for Linux distribution teams managing RPM builds, container images, AI code generation, test invocation, and merge request reviews.

Written in Go. Backed by PostgreSQL. Deployed on OpenShift/Kubernetes.

## How it works

Factory is a **platform**, not an application. It provides scheduling, lifecycle management, and observability. Domain-specific logic lives in **reconcilers** — separate Go projects in separate repositories.

Each work type runs as an independent pipeline:

```
your service → receiver /enqueue → database → dispatcher → reconciler
```

**Pure workqueue**: the queue stores only keys, never payloads. When a reconciler processes a key, it fetches current state from its own source of truth (Git, a registry, an API). This makes deduplication trivial and reconciliation naturally idempotent.

**3-service split**: each pipeline consists of three cooperating services:
- **Receiver** — accepts enqueue requests via HTTP, writes keys to the queue
- **Dispatcher** — claims items, manages lifecycle (leases, retries, dead-lettering), invokes the reconciler
- **Reconciler** — does the actual work, returns a result (completed, converged, requeue, fan-out, reject)

The receiver and dispatcher are **generic factory binaries** configured via environment variables. Only the reconciler contains domain-specific logic.

## Quick start

```bash
# Start the full stack with PostgreSQL
cd deploy
docker compose -f docker-compose.postgres.yaml up --build -d

# Enqueue a work item
curl -X POST http://localhost:8081/enqueue \
  -d '{"key":"curl-1.0-1.fc43","priority":10}'

# Check queue status
curl http://localhost:18080/admin/queues/echo

# Watch the reconciler process it
docker compose -f docker-compose.postgres.yaml logs echo-reconciler -f

# Tear down
docker compose -f docker-compose.postgres.yaml down -v
```

## Kubernetes deployment

The Kustomize base in `deploy/kubernetes/base/` provides the workqueue services (admin, receiver, dispatcher, HPA) and an in-cluster PostgreSQL. For production, use the postgres component overlay or bring your own PostgreSQL (RDS, Aurora, Cloud SQL).

### With in-cluster PostgreSQL

```bash
# Create an overlay for your queue
mkdir -p my-overlay
cat > my-overlay/kustomization.yaml << 'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: factory
resources:
  - github.com/hummingbird-org/factory-workqueue/deploy/kubernetes/base
components:
  - github.com/hummingbird-org/factory-workqueue/deploy/kubernetes/components/postgres
patches:
  - patch: |
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: factory-dispatcher
      spec:
        template:
          spec:
            containers:
              - name: dispatcher
                env:
                  - name: FACTORY_QUEUE_NAME
                    value: my-queue
                  - name: RECONCILER_ENDPOINT
                    value: http://my-reconciler:8082
    target:
      kind: Deployment
      name: factory-dispatcher
  - patch: |
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: factory-receiver
      spec:
        template:
          spec:
            containers:
              - name: receiver
                env:
                  - name: FACTORY_QUEUE_NAME
                    value: my-queue
    target:
      kind: Deployment
      name: factory-receiver
EOF

kubectl create namespace factory
kubectl apply -k my-overlay/
```

### With external PostgreSQL (RDS, Aurora, Cloud SQL)

Skip the postgres component and provide your own Secret:

```yaml
# my-overlay/kustomization.yaml
resources:
  - github.com/hummingbird-org/factory-workqueue/deploy/kubernetes/base
  - db-secret.yaml
# patches for queue name, reconciler endpoint...

# my-overlay/db-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: factory-db
type: Opaque
stringData:
  uri: "postgres://admin:password@mydb.abc123.rds.amazonaws.com:5432/factory?sslmode=require"
```

### Example overlay

See `deploy/kubernetes/overlays/kind-echo/` for a complete working example with an echo reconciler.

## Project structure

```
factory-workqueue/
├── cmd/
│   ├── receiver/          Generic receiver binary
│   ├── dispatcher/        Generic dispatcher binary
│   ├── admin/             Cross-queue admin API
│   └── factoryctl/        CLI admin tool
├── internal/
│   ├── store/             Unified persistence interface
│   │   ├── postgres/      PostgreSQL backend (production)
│   │   ├── dynamodb/      DynamoDB+S3 backend (AWS serverless)
│   │   ├── sqlite/        SQLite backend (single-node, edge)
│   │   ├── inmem/         In-memory backend (testing)
│   │   └── conformance/   Conformance suite all backends must pass
│   ├── dispatcher/        Dispatch/sweep/reaper loops + auto-heartbeat
│   ├── completion/        Retry, backoff, dead-letter, reject logic
│   ├── admin/             Admin API HTTP handlers
│   ├── wqapi/             Workqueue HTTP API handlers
│   ├── authz/             Pluggable authorization interface
│   │   ├── noop/          Allow everything (default)
│   │   ├── cedar/         Cedar policies (in-process)
│   │   └── opa/           Open Policy Agent (external server)
│   ├── authn/             Pluggable authentication interface
│   ├── storeutil/         Store creation from env vars
│   ├── authzutil/         Authorizer creation from env vars
│   ├── envutil/           Environment variable helpers
│   ├── httputil/          HTTP middleware (security headers)
│   ├── logging/           Structured logging setup
│   ├── metrics/           Prometheus metric definitions
│   └── tracing/           OpenTelemetry tracing setup
├── pkg/
│   └── types/             Shared data types (WorkItem, Status, etc.)
├── sdk/
│   └── go/
│       ├── reconciler/    Public SDK for reconciler authors (separate Go module)
│       ├── client/        HTTP clients for workqueue and reconciler
│       └── resync/        Deterministic resync sharder for periodic reconciliation
├── examples/
│   ├── echo-reconciler/   HTTP server reconciler (K8s, push model)
│   └── standalone-worker/ Self-dispatching worker (EC2, pull model)
├── tests/
│   ├── integration/       End-to-end platform tests (inmem store)
│   ├── sdk-conformance/   SDK conformance tests
│   └── container/         Container build tests
├── deploy/
│   ├── kubernetes/
│   │   ├── base/              Kustomize base (services + PostgreSQL)
│   │   ├── components/
│   │   │   └── postgres/      PostgreSQL component overlay
│   │   └── overlays/
│   │       ├── kind-echo/     Example: kind cluster with echo reconciler
│   │       └── openshift/     OpenShift with Route + Cedar
│   ├── docker-compose.postgres.yaml
│   ├── docker-compose.sqlite.yaml
│   ├── docker-compose.dynamodb.yaml
│   ├── docker-compose.cedar.yaml
│   ├── docker-compose.opa.yaml
│   ├── docker-compose.stress.yaml
│   └── policies/
│       ├── cedar/          Example Cedar policies
│       └── opa/            Example OPA/Rego policies
└── docs/
    ├── SDK.md              Reconciler SDK guide and wire protocol
    ├── SCALING.md          Capacity planning and tuning guide
    └── MONITORING.md       Metrics, alerts, and dashboards
```

## Store backends

All persistence flows through `store.Interface`. Swap backends by setting `STORE_BACKEND`:

| Backend | `STORE_BACKEND` | Use case | Claiming latency |
|---------|----------------|----------|-----------------|
| PostgreSQL | `postgres` | Production, high-throughput | <5ms |
| DynamoDB+S3 | `dynamodb` | AWS serverless | 5-50ms |
| SQLite | `sqlite` | Single-node, edge, development | <0.5ms |
| In-memory | (testing only) | Unit tests | <1us |

All backends pass the same conformance suite. Adding a new backend means implementing `store.Interface` and passing the suite.

## Authorization

Authorization is pluggable via `AUTHZ_BACKEND` — same pattern as store backends. Authentication is handled externally by an OAuth proxy (e.g., OpenShift OAuth Proxy). See [docs/AUTH.md](docs/AUTH.md) for details.

| Backend | `AUTHZ_BACKEND` | Description |
|---------|-----------------|-------------|
| Noop | `noop` (default) | Allow everything |
| Cedar | `cedar` | In-process policy evaluation (`AUTHZ_CEDAR_POLICY_PATH`) |
| OPA | `opa` | External OPA server (`AUTHZ_OPA_ENDPOINT`) |

## Writing a reconciler

See [docs/SDK.md](docs/SDK.md) for the full guide, including the wire protocol, Go SDK, non-Go examples, and lifecycle details.

Quick start with the Go SDK (zero external dependencies):

```bash
go get github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler
```

```go
import "github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

mux.Handle("POST /process", reconciler.ReconcilerHandler(func(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    if err := doWork(ctx, req.Key); err != nil {
        return reconciler.ProcessResponse{}, err // retriable failure
    }
    return reconciler.Completed(), nil
}))
```

Python and Rust SDKs are also available — see [docs/SDK.md](docs/SDK.md).

## Environment variables

### FACTORY_ (shared)

| Variable | Default | Description |
|----------|---------|-------------|
| `FACTORY_QUEUE_NAME` | (required) | Queue to operate on (required for receiver, dispatcher) |
| `FACTORY_WORKER_ID` | hostname | Worker identifier |
| `FACTORY_LISTEN_ADDR` | `:8080` or `:8081` | HTTP listen address |
| `FACTORY_LOG_FORMAT` | `json` | Log format: `json` or `text` |
| `FACTORY_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. Set `warn` in production to eliminate hot-path INFO logs. |
| `FACTORY_AUDIT_LOG` | (stderr) | Path to audit log file. When set, authorization events write here instead of stderr. |

### STORE_ (backend selection)

| Variable | Default | Description |
|----------|---------|-------------|
| `STORE_BACKEND` | `postgres` | `postgres`, `dynamodb`, or `sqlite` |

### PG_ (PostgreSQL)

| Variable | Default | Description |
|----------|---------|-------------|
| `PG_DATABASE_URL` | | PostgreSQL connection string (required for postgres backend) |
| `PG_MAX_CONNS` | `20` | Max pool connections |
| `PG_MIN_CONNS` | `2` | Min pool connections |
| `PG_MAX_CONN_LIFETIME` | `30m` | Max connection lifetime |
| `PG_HEALTH_CHECK_PERIOD` | `30s` | Health check interval |

### DDB_ / S3_ (DynamoDB)

| Variable | Default | Description |
|----------|---------|-------------|
| `DDB_TABLE` | | DynamoDB table name |
| `S3_BUCKET` | | S3 bucket for history |

### SQLITE_ (SQLite)

| Variable | Default | Description |
|----------|---------|-------------|
| `SQLITE_PATH` | | SQLite database path |

### DISPATCH_ (dispatcher)

| Variable | Default | Description |
|----------|---------|-------------|
| `DISPATCH_MODE` | `push` | `push` or `sweep-only` |
| `DISPATCH_INTERVAL` | `2s` | How often the dispatcher checks for work |
| `DISPATCH_BATCH_SIZE` | `10` | Items claimed per dispatch cycle |
| `DISPATCH_MAX_CONCURRENCY` | `10` | Max items in-flight simultaneously |
| `DISPATCH_MAX_RETRY` | `5` | Attempts before dead-lettering |
| `DISPATCH_LEASE_DURATION` | `1h` | Lease granted to claimed items |
| `DISPATCH_SWEEP_INTERVAL` | `60s` | How often the sweep loop runs |

### RECEIVER_ (receiver)

| Variable | Default | Description |
|----------|---------|-------------|
| `RECEIVER_MAX_QUEUE_DEPTH` | | Max pending items before rejecting enqueue |

### RECONCILER_ (reconciler connection)

| Variable | Default | Description |
|----------|---------|-------------|
| `RECONCILER_ENDPOINT` | (required in push mode) | Base URL of reconciler service |
| `RECONCILER_CA_CERT` | | PEM CA cert for reconciler TLS |

### AUTHN_ / AUTHZ_ (authentication & authorization)

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTHN_BACKEND` | `noop` | `noop` or `openshift` |
| `AUTHZ_BACKEND` | `noop` | `noop`, `cedar`, or `opa` |
| `AUTHZ_CEDAR_POLICY_PATH` | | Cedar policy file or directory (cedar backend) |
| `AUTHZ_OPA_ENDPOINT` | | OPA server URL (opa backend) |
| `AUTHZ_OPA_CA_CERT` | | PEM CA cert for OPA TLS (opa backend) |

## Admin API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/queues` | List all queues with item counts |
| GET | `/admin/queues/{name}` | Queue details |
| GET | `/admin/queues/{name}/items` | List items (filter: `?status=pending`) |
| GET | `/admin/queues/{name}/items/{key}` | Item detail with full history |
| POST | `/admin/queues/{name}/items/{key}/retry` | Retry a failed/dead-lettered item |
| POST | `/admin/queues/{name}/items/{key}/cancel` | Cancel an item |
| DELETE | `/admin/queues/{name}/dead-letters` | Purge dead-lettered items |
| POST | `/admin/queues/{name}/pause` | Pause a queue (items enqueue but don't dispatch) |
| POST | `/admin/queues/{name}/resume` | Resume a paused queue |
| GET | `/admin/workers` | List workers (filter: `?queue=name`) |
| GET | `/admin/queues/{name}/events` | SSE real-time event stream |

## CLI

```
factoryctl queues                        List all queues
factoryctl queues <name>                 Show queue details
factoryctl items <queue>                 List items
factoryctl items <queue> <key>           Item details + history
factoryctl items <queue> -s pending      Filter by status
factoryctl retry <queue> <key>           Retry a failed item
factoryctl cancel <queue> <key>          Cancel an item
factoryctl purge <queue>                 Purge dead letters
factoryctl pause <queue>                 Pause a queue
factoryctl resume <queue>                Resume a paused queue
factoryctl workers                       List all workers
factoryctl events <queue>                Stream real-time events
```

## Testing

```bash
# Run all offline tests
go test ./...

# Run PostgreSQL conformance (requires running PostgreSQL)
PG_DATABASE_URL="postgres://..." go test ./internal/store/postgres/ -v

# Run DynamoDB conformance (requires DynamoDB Local + rustfs)
DDB_TEST_ENDPOINT=http://localhost:8000 S3_TEST_ENDPOINT=http://localhost:9000 \
  go test ./internal/store/dynamodb/ -v

# Run stress test (10,000 items)
cd deploy && docker compose -f docker-compose.stress.yaml up --build -d
# Then enqueue with: seq 1 10000 | xargs -P100 -I{} curl -sf -o /dev/null \
#   -X POST http://localhost:8081/enqueue -d '{"key":"stress-{}","priority":0}'
```

## Documentation

- [SDK.md](docs/SDK.md) — reconciler SDK guide, wire protocol, Go and non-Go examples
- [SCALING.md](docs/SCALING.md) — capacity planning, HPA configuration, PostgreSQL tuning, queue isolation
- [MONITORING.md](docs/MONITORING.md) — Prometheus metrics, alerting rules, Grafana dashboards, structured logging
- [AUTH.md](docs/AUTH.md) — authentication, authorization, Cedar/OPA policy examples
- [CLAUDE.md](CLAUDE.md) — development conventions and project principles

## Design principles

- Pure workqueue (keys only, no payloads)
- Single queue per reconciler
- 3-service split (receiver/dispatcher/reconciler)
- Reconciliation pattern (desired vs actual state)
# Konflux Test
