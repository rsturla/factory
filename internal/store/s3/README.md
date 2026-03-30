# S3 Store Backend

The S3 backend stores all work queue state as objects in an S3 bucket (or any S3-compatible store like MinIO, rustfs, or Ceph RGW). It trades claiming speed for zero infrastructure overhead.

## How it works

### State machine via key prefixes

Work items move between S3 key prefixes as they transition through the state machine:

```
s3://factory-bucket/
  {queue}/queued/{key}          ← pending
  {queue}/in-progress/{key}    ← claimed or running
  {queue}/succeeded/{key}      ← completed successfully
  {queue}/failed/{key}         ← failed (awaiting retry or dead-letter)
  {queue}/dead-letter/{key}    ← permanently failed
  {queue}/history/{key}/{ts}   ← audit log entries (JSON bodies)
  _queues/{queue}              ← queue config (JSON body)
```

### Object metadata

Each work item object has an empty body. All state is stored in S3 user metadata headers:

| Metadata Key | Example | Purpose |
|-------------|---------|---------|
| `priority` | `50000042` | Zero-padded + offset for lexicographic sorting |
| `priority-raw` | `42` | Actual priority value |
| `attempts` | `3` | Current attempt count |
| `max-attempts` | `5` | Max before dead-lettering |
| `status` | `claimed` | Sub-status within a prefix (e.g., claimed vs running) |
| `not-before` | `2026-03-30T12:00:00Z` | Earliest eligible time |
| `lease-expires` | `2026-03-30T13:00:00Z` | Lease expiration |
| `worker-id` | `dispatcher-abc` | Claiming dispatcher |
| `created-at` | `2026-03-30T11:00:00Z` | Enqueue time |
| `claimed-at` | `2026-03-30T11:05:00Z` | Claim time |

### Claiming

1. `ListObjectsV2` on `{queue}/queued/` to find eligible items
2. `HeadObject` on each to read metadata (priority, not-before)
3. Filter out items whose `not-before` is in the future
4. Sort client-side by priority DESC, created_at ASC
5. For each item to claim: `PutObject` to `in-progress/` + `DeleteObject` from `queued/`

This is **not atomic**. If the process crashes between the put and delete, the item exists in both prefixes. The reaper loop detects and repairs this.

### Deduplication

Enqueue checks for an existing object via `HeadObject`. If found in `queued/`, the priority is merged upward and the object is overwritten.

### History

Each state transition is written as a separate JSON object under `{queue}/history/{key}/{timestamp}`. Querying history means listing and reading these objects. Not as fast as a database query, but durable and simple.

### Events

No native push mechanism. The `Subscribe` method polls `ListObjectsV2` every 5 seconds and emits events for newly appearing keys. Higher latency than PostgreSQL's LISTEN/NOTIFY.

### Concurrency control

Enforced by counting objects in `{queue}/in-progress/` via `ListObjectsV2` before each claim batch. No meta-row lock — concurrent dispatchers may briefly over-claim during races, but the max_concurrency limit is eventually consistent.

## When to use it

- **Batch/bulk queues** where claiming latency of 100-500ms is acceptable (nightly rebuilds, weekly scans, bulk imports).
- **Edge or satellite deployments** where running PostgreSQL is impractical but S3 (or MinIO) is available.
- **Disaster recovery** as a secondary store that survives total cluster loss.
- **Cost-sensitive environments** where you want zero idle compute cost for the queue infrastructure.
- **S3-compatible stores** like MinIO, rustfs, Ceph RGW, or Backblaze B2 — any store supporting `PutObject`, `HeadObject`, `ListObjectsV2`, `DeleteObject`.

## When NOT to use it

- **Primary high-throughput workloads** — claiming is orders of magnitude slower than PostgreSQL SKIP LOCKED.
- **Workloads requiring strong consistency** — S3 is eventually consistent for listing after writes in some configurations.
- **Real-time dashboards** — the 5-second polling interval for events is too slow for live monitoring.
- **Workloads with deep queues** (10k+ pending items) — every claim cycle lists and sorts all queued items client-side.

## Configuration

| Environment Variable | Required | Description |
|---------------------|----------|-------------|
| `S3_BUCKET` | yes | S3 bucket name |
| `AWS_REGION` | no | AWS region (uses SDK default if unset) |
| `S3_ENDPOINT` | no | Custom endpoint for S3-compatible stores |
| `AWS_ACCESS_KEY_ID` | yes | AWS credentials |
| `AWS_SECRET_ACCESS_KEY` | yes | AWS credentials |

For S3-compatible stores (MinIO, rustfs):
```
S3_BUCKET=factory-queue S3_ENDPOINT=http://minio:9000 AWS_ACCESS_KEY_ID=admin AWS_SECRET_ACCESS_KEY=admin
```

## Performance characteristics

| Operation | Typical latency | Scaling behavior |
|-----------|----------------|------------------|
| Enqueue | 10-50ms | Constant (HeadObject + PutObject) |
| ClaimBatch (10 items) | 100-500ms | O(queue depth) — lists all queued items |
| Complete/Fail | 10-30ms | Constant (PutObject + DeleteObject) |
| CountByStatus | 50-200ms | O(items) — lists each prefix |
| List (100 items) | 100-500ms | O(items) — lists + HeadObject per item |
| History query | 50-200ms | O(entries) — lists + GetObject per entry |

## Conformance

This backend passes the full 21-test conformance suite. Tested against:
- **rustfs** (S3-compatible, Rust-based) via Podman
