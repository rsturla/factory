# PostgreSQL Store Backend

The PostgreSQL backend is the **primary, recommended store** for production deployments. It provides the strongest guarantees for correctness, performance, and observability.

## How it works

All state is stored in four tables within a single PostgreSQL database:

- **`work_items`** — The core queue table. Each row is a work item identified by `(queue, key)`. Status, priority, attempts, timestamps, and lease information are columns.
- **`queue_state`** — One row per queue holding configuration (max concurrency, max retry) and a cached `in_progress` counter for O(1) concurrency checks.
- **`work_item_history`** — Append-only audit log of every state transition, with trace IDs for correlation.
- **`worker_leases`** — Worker registration and heartbeat tracking.

### Claiming

Uses `SELECT FOR UPDATE SKIP LOCKED` — the PostgreSQL-native pattern for work queues:

```sql
UPDATE work_items
SET status = 'claimed', ...
WHERE (queue, key) IN (
    SELECT queue, key FROM work_items
    WHERE queue = $1 AND status = 'pending'
    ORDER BY priority DESC, created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT $2
)
```

This is **zero-contention**: multiple dispatchers can claim from the same queue concurrently without blocking each other. Locked rows are silently skipped, not waited on.

### Concurrency control

The `queue_state.in_progress` counter is locked with `FOR UPDATE` at the start of each claim transaction to enforce `max_concurrency`. The sweep loop periodically reconciles this counter against actual row counts via `RepairCounter`.

### Deduplication

Enqueue uses `INSERT ... ON CONFLICT DO UPDATE` with a `WHERE status = 'pending'` guard. If the key already exists and is pending, only the priority is merged upward. Non-pending items are not overwritten.

### Events

Real-time state change notifications use PostgreSQL `LISTEN/NOTIFY`. A trigger on the `work_items` table fires `pg_notify` on every status change, which the admin API streams as Server-Sent Events.

### History

State transitions are recorded inline within the same transaction as the status change — no separate write path, no risk of losing history if the process crashes between the status update and the history write.

## When to use it

- **Always, unless you have a specific reason not to.** This is the default.
- High-throughput workloads (thousands of claims/second).
- Workloads requiring strong consistency and exactly-once claiming.
- Deployments where you need the admin API's full query capabilities (filtered listing, history, SSE streaming).
- Any deployment on OpenShift/Kubernetes where CrunchyData PGO or similar operators manage PostgreSQL.

## When NOT to use it

- Edge deployments without database infrastructure — consider the S3 backend.
- Environments where PostgreSQL operational overhead is unacceptable — consider the S3 backend or in-memory backend for testing.

## Configuration

| Environment Variable | Required | Description |
|---------------------|----------|-------------|
| `DATABASE_URL` | yes | PostgreSQL connection string (e.g., `postgres://user:pass@host:5432/factory`) |

The store runs schema migrations automatically on startup via `Migrate()`.

## Performance characteristics

| Operation | Typical latency | Scaling behavior |
|-----------|----------------|------------------|
| Enqueue | <1ms | Constant (upsert) |
| ClaimBatch (10 items) | 2-5ms | Constant via SKIP LOCKED |
| Complete/Fail | <1ms | Constant |
| CountByStatus | 1-2ms | O(distinct statuses) via partial index |
| List (100 items) | 2-5ms | O(limit) via index scan |
| History query | 1-3ms | O(entries) via index |

Recommended PostgreSQL configuration for production:
- `max_connections`: 200+
- `shared_buffers`: 25% of RAM
- Connection pooling via PgBouncer or pgxpool (built-in)
