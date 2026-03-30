# SQLite Store Backend

The SQLite backend stores all work queue state in a single local database file. It uses the same SQL schema as PostgreSQL (with minor syntax adjustments) and provides full ACID transactions. Built with `modernc.org/sqlite` — a pure-Go SQLite implementation, no CGo required.

## How it works

### Schema

Same four tables as PostgreSQL: `work_items`, `work_item_history`, `worker_leases`, and `queue_state`. Timestamps are stored as RFC3339 text strings (SQLite has no native timestamp type).

### Claiming

SQLite doesn't have `SELECT FOR UPDATE SKIP LOCKED`, but it doesn't need it — all writes are serialized through a Go mutex and SQLite's file-level write lock. The claim sequence is:

1. Lock the mutex
2. `BEGIN` transaction
3. `SELECT` pending items ordered by priority
4. `UPDATE` each to claimed
5. Increment `in_progress` counter
6. `COMMIT`

In a single-process context this is equivalent to SKIP LOCKED — no contention is possible.

### WAL mode

The store enables WAL (Write-Ahead Logging) mode on startup, which allows concurrent reads while a write transaction is active. This means the admin API can query queue depth while the dispatcher is claiming items.

### Events

Uses the same in-process channel-based subscription as the in-memory backend. State-changing operations emit events to subscribers. No external notification mechanism.

## When to use it

- **Single-node deployments** — a dispatcher + reconciler on one machine with a local queue file.
- **Edge/satellite workers** — workers in environments without database or S3 access. The queue survives process restarts (unlike in-memory).
- **Development** — faster to start than PostgreSQL, provides durability for debugging.
- **Embedded systems** — factory running as a single binary on bare metal or IoT devices.
- **Testing with durability** — when you want to test crash recovery scenarios that in-memory can't cover.

## When NOT to use it

- **Multi-process deployments** — SQLite's file lock prevents multiple dispatchers from running concurrently. Use PostgreSQL for multi-instance.
- **High-throughput** — the write mutex serializes all mutations. Fine for hundreds of items/second, not thousands.
- **Kubernetes/OpenShift** — pods typically don't have persistent local storage. Use PostgreSQL with PGO, or S3.

## Configuration

```go
// File-backed (durable):
s, err := sqlite.New("/var/lib/factory/queue.db")

// In-memory (testing):
s, err := sqlite.New(":memory:")
```

The store runs schema migrations automatically on creation. No separate migration step needed.

## Performance characteristics

| Operation | Typical latency |
|-----------|----------------|
| Enqueue | <100 microseconds |
| ClaimBatch (10 items) | <500 microseconds |
| Complete/Fail | <100 microseconds |
| CountByStatus | <50 microseconds |
| List (100 items) | <200 microseconds |
| History query | <100 microseconds |

All operations are sub-millisecond for typical workloads. The write mutex limits throughput to ~1000-5000 writes/second depending on disk speed (or effectively unlimited for `:memory:`).

## Conformance

This backend passes the full 31-test conformance suite using in-memory SQLite databases (30 pass, 1 skip for `ReEnqueueAfterComplete`).
