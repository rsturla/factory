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
- **Reconciler** — does the actual work, returns a result (completed, converged, requeue, fan-out)

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

## Project structure

```
factory-v2/
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
│   │   └── conformance/   31-test suite all backends must pass
│   ├── dispatcher/        Dispatch/sweep/reaper/scale loops
│   ├── completion/        Retry, backoff, dead-letter logic
│   ├── compute/           Compute provider abstraction
│   │   ├── kubernetes/    Scale K8s Deployments
│   │   └── ec2/           Scale AWS Auto Scaling Groups
│   ├── admin/             Admin API HTTP handlers
│   ├── authz/             Pluggable authorization interface
│   │   ├── noop/          Allow everything (default)
│   │   ├── cedar/         Cedar policies (in-process)
│   │   └── opa/           Open Policy Agent (external server)
│   ├── storeutil/         Store creation from env vars
│   ├── authzutil/         Authorizer creation from env vars
│   ├── metrics/           Prometheus metric definitions
├── pkg/
│   ├── sdk/               Public SDK for reconciler authors
│   └── client/            HTTP clients for inter-service communication
├── examples/
│   ├── echo-reconciler/   HTTP server reconciler (K8s, push model)
│   └── standalone-worker/ Self-dispatching worker (EC2, pull model)
├── deploy/
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

All backends pass the same 31-test conformance suite. Adding a new backend means implementing `store.Interface` and passing the suite.

## Authorization

Authorization is pluggable via `AUTHZ_BACKEND` — same pattern as store backends. Authentication is handled externally by an OAuth proxy (e.g., OpenShift OAuth Proxy). See [docs/AUTH.md](docs/AUTH.md) for details.

| Backend | `AUTHZ_BACKEND` | Description |
|---------|-----------------|-------------|
| Noop | `noop` (default) | Allow everything |
| Cedar | `cedar` | In-process policy evaluation (`AUTHZ_CEDAR_POLICY_PATH`) |
| OPA | `opa` | External OPA server (`AUTHZ_OPA_ENDPOINT`) |

## Writing a reconciler

Two patterns depending on how long the work takes and where it runs:

| Duration | Pattern | Dispatcher mode | Example |
|----------|---------|----------------|---------|
| Under 2 minutes | HTTP reconciler | `push` | MR review, test runner, API calls |
| 2-60 minutes | Either | Depends | Container build (K8s → push, EC2 → standalone) |
| Over 60 minutes | Standalone worker | `scale-only` | RPM build, AI inference |

For work that delegates to external systems (Koji, Tekton, CI) and polls for completion, use an HTTP reconciler with `RequeueAfter` regardless of how long the external work takes — each invocation is a quick status check.

### HTTP reconciler (push model)

The dispatcher calls your `/process` endpoint. The reconciler does the work inline and returns. Set `DISPATCH_MODE=push` (default). See `examples/echo-reconciler/`.

```go
import "github.com/hummingbird-org/factory-workqueue/pkg/sdk"

func main() {
    mux := http.NewServeMux()
    mux.Handle("POST /process", sdk.ReconcilerHandler(reconcile))
    http.ListenAndServe(":8082", mux)
}

func reconcile(ctx context.Context, req sdk.ProcessRequest) (sdk.ProcessResponse, error) {
    if alreadyDone(ctx, req.Key) {
        return sdk.Converged(), nil
    }
    if err := doWork(ctx, req.Key); err != nil {
        return sdk.ProcessResponse{}, err
    }
    return sdk.Completed(), nil
}
```

### Standalone worker (pull model)

The worker claims items via the workqueue HTTP API, processes them locally, and reports back. Set `DISPATCH_MODE=scale-only` on the dispatcher. See `examples/standalone-worker/`.

```go
import "github.com/hummingbird-org/factory-workqueue/pkg/client"

wq := client.NewWorkqueueClient("http://factory-receiver:8081")

for {
    items, _ := wq.ClaimBatch(ctx, "rpm-update", 1, workerID, 2*time.Hour)
    if len(items) == 0 {
        time.Sleep(5 * time.Second)
        continue
    }

    // Heartbeat in background to keep lease alive
    go heartbeat(ctx, wq, item)

    // Do heavy local work (minutes/hours)
    if err := rpmbuild(item.Key); err != nil {
        wq.Fail(ctx, item.Queue, item.Key, err.Error())
    } else {
        wq.Complete(ctx, item.Queue, item.Key)
    }
}
```

Both patterns use the same store, same state machine, same retry/dead-letter logic. If a worker dies mid-work, the lease expires and the reaper reclaims the item. Workers exit after `MAX_IDLE` (default 10m) with no work, and the dispatcher scales compute back down.

Available responses:
- `sdk.Completed()` — work done successfully
- `sdk.Converged()` — desired state already met, nothing to do
- `sdk.RequeueAfter(duration)` — check back later (doesn't consume retry budget)
- `sdk.FanOut(keys...)` — complete this item and enqueue dependent items
- Return an `error` — retriable failure, requeued with exponential backoff

## Environment variables

### All binaries

| Variable | Default | Description |
|----------|---------|-------------|
| `STORE_BACKEND` | `postgres` | `postgres`, `dynamodb`, or `sqlite` |
| `DATABASE_URL` | | PostgreSQL connection string (postgres backend) |
| `DDB_TABLE` | | DynamoDB table name (dynamodb backend) |
| `S3_BUCKET` | | S3 bucket for history (dynamodb backend) |
| `SQLITE_PATH` | | SQLite database path (sqlite backend) |
| `AUTHZ_BACKEND` | `noop` | `noop`, `cedar`, or `opa` |
| `AUTHZ_CEDAR_POLICY_PATH` | | Cedar policy file or directory (cedar backend) |
| `AUTHZ_OPA_ENDPOINT` | | OPA server URL (opa backend) |

### Receiver

| Variable | Default | Description |
|----------|---------|-------------|
| `QUEUE_NAME` | (required) | Queue to enqueue into |
| `LISTEN_ADDR` | `:8081` | HTTP listen address |

### Dispatcher

| Variable | Default | Description |
|----------|---------|-------------|
| `QUEUE_NAME` | (required) | Queue to dispatch |
| `RECONCILER_ENDPOINT` | (required) | Base URL of reconciler service |
| `WORKER_ID` | hostname | Unique dispatcher identifier |
| `COMPUTE_BACKEND` | `noop` | `noop`, `kubernetes`, or `ec2` |
| `MAX_CONCURRENCY` | `10` | Max items in-flight simultaneously |
| `BATCH_SIZE` | `10` | Items claimed per dispatch cycle |
| `DISPATCH_INTERVAL` | `2s` | How often the dispatcher checks for work |
| `MAX_RETRY` | `5` | Attempts before dead-lettering |
| `LEASE_DURATION` | `1h` | Lease granted to claimed items |

### Admin API

| Variable | Default | Description |
|----------|---------|-------------|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |

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
factoryctl workers                       List all workers
factoryctl events <queue>                Stream real-time events
```

## Testing

```bash
# Run all offline tests (171 tests)
go test ./...

# Run PostgreSQL conformance (requires running PostgreSQL)
DATABASE_URL="postgres://..." go test ./internal/store/postgres/ -v

# Run DynamoDB conformance (requires DynamoDB Local + rustfs)
DDB_TEST_ENDPOINT=http://localhost:8000 S3_TEST_ENDPOINT=http://localhost:9000 \
  go test ./internal/store/dynamodb/ -v

# Run stress test (10,000 items)
cd deploy && docker compose -f docker-compose.stress.yaml up --build -d
# Then enqueue with: seq 1 10000 | xargs -P100 -I{} curl -sf -o /dev/null \
#   -X POST http://localhost:8081/enqueue -d '{"key":"stress-{}","priority":0}'
```

## Documentation

- [SCALING.md](docs/SCALING.md) — capacity planning, HPA configuration, PostgreSQL tuning, queue isolation
- [MONITORING.md](docs/MONITORING.md) — Prometheus metrics, alerting rules, Grafana dashboards, structured logging
- [AUTH.md](docs/AUTH.md) — authentication, authorization, Cedar/OPA policy examples
- [CLAUDE.md](CLAUDE.md) — development conventions and project principles

## Design principles

- Pure workqueue (keys only, no payloads)
- Single queue per reconciler
- 3-service split (receiver/dispatcher/reconciler)
- Reconciliation pattern (desired vs actual state)
