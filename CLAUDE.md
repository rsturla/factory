# Factory V2

A pure workqueue platform for orchestrating software factory operations (RPM builds, container images, AI code generation, tests, MR reviews). Written in Go, deployed on OpenShift.

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
- `internal/store/dynamodb/` — DynamoDB+S3 hybrid implementation (AWS serverless).
- `internal/store/sqlite/` — SQLite implementation (single-node, edge, dev).
- `internal/store/inmem/` — In-memory implementation (unit tests).
- `internal/store/conformance/` — 31-test suite. All store implementations must pass.
- `internal/storeutil/` — Store creation from `STORE_BACKEND` env var. Used by all binaries.
- `internal/dispatcher/` — Dispatch/sweep/reaper/scale loops.
- `internal/completion/` — Retry, backoff, dead-letter decisions.
- `internal/compute/` — Compute provider abstraction (K8s, EC2, extensible).
- `internal/admin/` — Admin API HTTP handlers.
- `internal/metrics/` — Prometheus metric definitions.
- `pkg/sdk/` — Public SDK: ProcessRequest, ProcessResponse, ReconcilerHandler, response builders.
- `pkg/client/` — HTTP clients for inter-service communication.

## Data layer

All persistence is abstracted behind `store.Interface`. To swap the storage backend:
1. Implement `store.Interface` in a new package (e.g., `internal/store/cockroachdb/`).
2. Pass the conformance test suite (`internal/store/conformance/`).
3. Add the backend to `internal/storeutil/create.go`.

No other code needs to change — dispatcher, completion, webhook, admin all accept `store.Interface`.

Current backends: `postgres` (default), `dynamodb`, `sqlite`, `inmem` (tests only).

## Testing

- `go test ./...` must pass. Currently 171 tests.
- The conformance suite (`internal/store/conformance/`) is the source of truth for store behavior (31 tests).
- New store implementations must pass the full conformance suite.
- PostgreSQL and DynamoDB conformance tests skip gracefully when services are unavailable.
- Dispatcher tests use inmem store + httptest reconciler server.
- SDK tests verify the public API contract for reconciler authors.

## Building

```
go build ./cmd/receiver/
go build ./cmd/dispatcher/
go build ./cmd/admin/
go build ./cmd/factoryctl/
```

Container images use `quay.io/hummingbird/go:1.26` for building and `quay.io/hummingbird/core-runtime:latest` for runtime.

## Running locally

```bash
# PostgreSQL (recommended)
cd deploy && docker compose -f docker-compose.postgres.yaml up --build -d

# SQLite (no external deps)
cd deploy && docker compose -f docker-compose.sqlite.yaml up --build -d

# DynamoDB+S3 (uses DynamoDB Local + rustfs)
cd deploy && docker compose -f docker-compose.dynamodb.yaml up --build -d

# Stress test (10k items, max throughput)
cd deploy && docker compose -f docker-compose.stress.yaml up --build -d
```

## Environment variables

All binaries accept `STORE_BACKEND` (`postgres`, `dynamodb`, `sqlite`) plus backend-specific variables. See [README.md](README.md) for the full reference.

## Documentation

- [README.md](README.md) — project overview, quick start, full API reference
- [docs/SCALING.md](docs/SCALING.md) — capacity planning for 500k+ jobs/day
- [docs/MONITORING.md](docs/MONITORING.md) — Prometheus metrics, alerting, dashboards
