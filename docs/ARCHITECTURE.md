# Architecture: Why a Workqueue?

## The problem

Software factories run heterogeneous operations: RPM builds, container image builds, AI-assisted code review, test suites, merge request automation. These operations share common needs:

- **Prioritization** — security patches before routine updates
- **Concurrency control** — don't overwhelm a build system or GPU pool
- **Retry with backoff** — transient failures shouldn't require human intervention
- **Observability** — operators need to see what's running, what failed, and why
- **Idempotency** — re-running an operation should converge to the same result

Message queues (SQS, RabbitMQ) solve the first half of this. But they carry payloads, not intent. A consumer processes a message and deletes it. If the consumer crashes, the message reappears — but the world may have changed since it was enqueued. The consumer has no way to know.

## The reconciler model

This workqueue uses the Kubernetes reconciler pattern: **keys, not payloads**.

A key is an identifier (e.g., `curl-8.7.1-2.fc43`). When a reconciler receives a key, it fetches the current state from its source of truth (a Git repo, a package registry, a container catalog) and compares it to the desired state. If they differ, it acts. If they match, it reports `converged`.

This gives you:

**Natural idempotency.** Re-enqueueing the same key is harmless. The reconciler will check current state and either do work or report converged. No duplicate processing, no "exactly-once" delivery gymnastics.

**No poison messages.** A message queue carries a payload that might be malformed, outdated, or impossible to process. A key just points to state. If the state changes between enqueue and processing, the reconciler sees the current state — not a stale snapshot frozen at enqueue time.

**Convergence under failure.** If a reconciler crashes mid-work, the lease expires, the reaper reclaims the key, and another reconciler picks it up. The new reconciler fetches current state (which may include partial work from the crash) and converges from there. No rollback needed.

**Deduplication for free.** If the same key is enqueued twice (e.g., two webhooks fire for the same package update), the queue merges them. Priority is merged upward. The reconciler runs once.

## How it compares

| Capability | SQS/RabbitMQ | Kinesis/Kafka | Factory Workqueue |
|-----------|-------------|--------------|-------------------|
| Delivery model | Message (payload) | Stream (log) | Key (identifier) |
| Ordering | FIFO or none | Per-shard | Priority + FIFO |
| Concurrency control | Consumer-side | Partition count | Per-queue MaxConcurrency |
| Retry semantics | Visibility timeout | Consumer offset | Exponential backoff + dead-letter |
| Idempotency | Consumer responsibility | Consumer responsibility | Built-in (key dedup + reconciler model) |
| Item-level visibility | Approximate count | Offset lag | Full status, history, error messages |
| Operational action | Purge queue | Reset offset | Retry/cancel individual items |
| Infrastructure | Managed service | Managed service | Postgres (or SQLite/DynamoDB) |

## When to use this

- Work items take seconds to hours (builds, tests, reviews), not milliseconds
- You need per-item observability (what failed, why, how many attempts)
- You need priority scheduling (security patches before routine updates)
- You need concurrency limits (don't overwhelm downstream systems)
- Your operations are naturally idempotent (reconcile to desired state)
- You want a single Postgres dependency, not a managed service

## When NOT to use this

- High-throughput event streaming (use Kafka/Kinesis)
- Simple async message passing (use SQS/RabbitMQ)
- Sub-millisecond latency requirements (use in-process queues)
- You need managed infrastructure with zero ops (use cloud services)

## Architecture overview

```
Webhook/CI ──► Receiver ──► PostgreSQL ◄── Dispatcher ──► Reconciler
                                ▲              │
                                │              ├── Reaper (reclaim expired leases)
                                │              ├── Sweep (update metrics)
                            Admin API          └── Scale (auto-scale workers)
```

**Receiver** accepts enqueue requests over HTTP. Stateless, horizontally scalable. Writes keys to the store.

**Dispatcher** claims keys from the store and dispatches them to reconcilers. Runs leader election per queue. Manages the full lifecycle: claim → dispatch → handle response → complete/retry/dead-letter.

**Reconciler** receives a key, fetches state, does work, returns a response. Lives in a separate repo with its own deployment. The workqueue platform knows nothing about RPMs, containers, or AI — it just manages keys.

**Store** is the single source of truth for queue state. PostgreSQL for production, SQLite for edge/single-node, DynamoDB for AWS serverless. All backends pass the same 47-test conformance suite.

## Data model

```
work_items (queue, key) ──── The work item and its lifecycle state
claim_queue (queue, key) ──── Side-table for pending items (enables HOT updates)
active_leases (queue, key) ── Side-table for lease tracking (reaper queries this)
queue_state (queue) ──────── Queue configuration and counters
work_item_history ─────────── Audit trail of state transitions
worker_leases ─────────────── Registered workers and heartbeats
```

The `claim_queue` and `active_leases` side-tables exist for Postgres performance. Status transitions on `work_items` are HOT (Heap-Only Tuple) updates because no index on `work_items` references the `status` column. This gives ~3x write throughput compared to a naive schema.
