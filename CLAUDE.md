# Factory V2

A pure workqueue platform for orchestrating software factory operations (RPM builds, container images, AI code generation, tests, MR reviews). Written in Go, backed by PostgreSQL, deployed on OpenShift.

## Architecture

- **Pure workqueue**: Keys only, no payloads. Reconcilers fetch state from their own source of truth.
- **Single queue per reconciler**: Each reconciler type owns an isolated queue.
- **3-service split**: Receiver (enqueue) → Dispatcher (claim/lifecycle) → Reconciler (do work).
- **Receiver and Dispatcher are generic factory binaries** configured via env vars. Reconcilers are separate repos.
- **Reconcilers are NOT in this repo.** They live in separate Go projects with their own `go.mod`, importing `pkg/sdk/`.

## Repo boundary

This repo is the **platform**. It must never contain domain-specific logic (RPM, container, codegen, etc.). The only public API surface for reconciler authors is `pkg/sdk/`.

## Code conventions

- Go standard library where possible. Minimal dependencies.
- `log/slog` for structured logging. JSON handler in production.
- `internal/` for platform internals. `pkg/` for importable public packages.
- PostgreSQL via `pgx/v5`. No ORM.
- Prometheus via `prometheus/client_golang`.
- `context.Context` on all interface methods.
- Functional options pattern for configurable operations (e.g., `EnqueueOption`).

## Key packages

- `internal/store/` — Unified persistence interface (`store.Interface`). All state flows through this.
- `internal/store/postgres/` — PostgreSQL implementation (production).
- `internal/store/inmem/` — In-memory implementation (testing).
- `internal/store/conformance/` — Shared test suite. All store implementations must pass.
- `internal/dispatcher/` — Dispatch/sweep/reaper/scale loops.
- `internal/completion/` — Retry, backoff, dead-letter decisions.
- `internal/compute/` — Compute provider abstraction (K8s, EC2, extensible).
- `internal/admin/` — Admin API HTTP handlers.
- `internal/webhook/` — Webhook handlers and key extractors.
- `pkg/sdk/` — Public SDK: ProcessRequest, ProcessResponse, ReconcilerHandler, response builders.
- `pkg/client/` — HTTP clients for inter-service communication.

## Data layer

All persistence is abstracted behind `store.Interface`. To swap the storage backend:
1. Implement `store.Interface` in a new package (e.g., `internal/store/cockroachdb/`).
2. Pass the conformance test suite (`internal/store/conformance/`).
3. Wire it up in the `cmd/` binaries.

No other code needs to change — dispatcher, completion, webhook, admin all accept `store.Interface`.

## Testing

- `go test ./...` must pass.
- The conformance suite (`internal/store/conformance/`) is the source of truth for store behavior.
- New store implementations must pass the full conformance suite (21 tests).
- Integration tests requiring PostgreSQL should use build tags or skip when no database is available.

## Building

```
go build ./cmd/receiver/
go build ./cmd/dispatcher/
go build ./cmd/admin/
go build ./cmd/factoryctl/
```

## Environment variables (dispatcher)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| QUEUE_NAME | yes | | Queue to dispatch |
| DATABASE_URL | yes | | PostgreSQL connection string |
| RECONCILER_ENDPOINT | yes | | Base URL of reconciler service |
| WORKER_ID | no | hostname | Unique dispatcher ID |
| COMPUTE_BACKEND | no | noop | "noop", "kubernetes", "ec2" |
| MAX_CONCURRENCY | no | 10 | Max concurrent items |
| MAX_RETRY | no | 5 | Max retry attempts |
| BATCH_SIZE | no | 10 | Items per dispatch cycle |
| DISPATCH_INTERVAL | no | 2s | Dispatch loop interval |
| LEASE_DURATION | no | 1h | Lease for claimed items |

## Environment variables (receiver)

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| QUEUE_NAME | yes | | Queue to enqueue into |
| DATABASE_URL | yes | | PostgreSQL connection string |
| WEBHOOK_SECRET | no | | HMAC secret for signature verification |
| WEBHOOK_SOURCE | no | github | "github" or "gitlab" |
| LISTEN_ADDR | no | :8081 | HTTP listen address |
