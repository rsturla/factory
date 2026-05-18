# Factory Workqueue

A pure workqueue platform for orchestrating software factory operations at scale. Manages the scheduling, lifecycle, and observability of work items while domain-specific logic lives in external reconcilers.

Written in Go. Backed by PostgreSQL. Deployed on OpenShift/Kubernetes.

## How it works

Factory is a **platform**, not an application. It stores only keys, never payloads. When a reconciler processes a key, it fetches current state from its own source of truth (Git, a registry, an API). This makes deduplication trivial and reconciliation naturally idempotent.

Each work type runs as an independent pipeline:

```
your service → receiver /enqueue → database → dispatcher → reconciler
```

## Components

### Core Services

- **Receiver** — Accepts enqueue requests via HTTP, writes keys to the queue.
- **Dispatcher** — Claims items, manages lifecycle (leases, retries, dead-lettering), invokes the reconciler.
- **Admin** — Cross-queue admin API for inspecting queues, retrying items, purging dead letters, pausing/resuming, and streaming events.
- **factoryctl** — CLI tool for queue administration.

### Data Layer

Persistence is abstracted behind a unified store interface with pluggable backends:

| Backend | Use case |
|---------|----------|
| PostgreSQL | Production, high-throughput |
| DynamoDB+S3 | AWS serverless |
| SQLite | Single-node, edge, development |
| In-memory | Unit tests |

All backends pass the same conformance test suite.

### Authorization

Pluggable authorization with three backends: noop (allow all), Cedar (in-process policy evaluation), and OPA (external policy server). Authentication is handled externally by an OAuth proxy.

### SDKs

Client libraries and reconciler handlers for building domain-specific workers:

- **Go** — `sdk/go/reconciler/` and `sdk/go/client/`
- **Python** — Sync and async clients, ASGI handlers
- **Rust** — Async clients (reqwest), axum handlers

### Observability

- Prometheus metrics for queue depth, claim latency, and reconciler outcomes
- OpenTelemetry tracing with trace ID propagation through the full pipeline
- Structured JSON logging via `log/slog`

## Documentation

- [SDK.md](docs/SDK.md) — Reconciler SDK guide, wire protocol, Go/Python/Rust examples
- [SCALING.md](docs/SCALING.md) — Capacity planning, HPA configuration, PostgreSQL tuning
- [MONITORING.md](docs/MONITORING.md) — Prometheus metrics, alerting rules, Grafana dashboards
- [AUTH.md](docs/AUTH.md) — Authentication, authorization, Cedar/OPA policy examples
