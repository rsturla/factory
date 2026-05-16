# DynamoDB + S3 Store Backend

A hybrid backend using DynamoDB for the hot path (queue mechanics) and S3 for the cold path (history). Combines DynamoDB's single-digit ms latency and atomic conditional writes with S3's cheap, durable storage for audit logs.

## How it works

### DynamoDB table schema

Single table design with a GSI for efficient claiming:

```
PK: "{queue}#{key}"           SK: "ITEM"      ← work items
PK: "_queue#{queue}"          SK: "CONFIG"    ← queue configuration

GSI "ClaimIndex":
  GSI1PK: "{queue}#{status}"
  GSI1SK: "{inverted_priority}#{created_at}"
```

Priority is inverted in the GSI sort key so that higher-priority items sort first (DynamoDB sorts ascending). This enables server-side priority ordering — no client-side sorting needed.

### Claiming

1. `Query` the ClaimIndex for `{queue}#pending` items, ordered by GSI1SK (highest priority first)
2. For each candidate: `UpdateItem` with `ConditionExpression: status = :pending`
3. If the condition fails, another dispatcher claimed it — skip to the next item

This is DynamoDB's equivalent of PostgreSQL's `SELECT FOR UPDATE SKIP LOCKED`. Each claim is a single atomic API call with conflict detection.

Concurrency limits are enforced via a strongly consistent `Scan` (not the eventually consistent GSI) to count in-progress items before claiming.

### State transitions

All status changes are `UpdateItem` with `ConditionExpression` matching the expected current status. This provides atomic compare-and-swap semantics — `Transition(claimed → running)` fails with `ErrConflict` if the item is no longer in `claimed` status.

### History (S3)

State transitions are recorded as individual JSON objects in S3:

```
s3://history-bucket/{queue}/history/{key}/{timestamp}_{random}
```

Each entry is a small JSON file. Querying history lists the prefix and reads each object. Random suffix prevents timestamp collisions for rapid transitions.

### Events

Polls DynamoDB for count changes every 2 seconds. DynamoDB Streams could provide push-based events but adds deployment complexity. The polling approach is simpler and sufficient for dashboard use.

## When to use it

- **AWS-native deployments** where you want serverless queue infrastructure without managing PostgreSQL.
- **Medium-throughput workloads** — faster than S3 (single-digit ms claims vs 100-500ms), simpler than PostgreSQL (no database to operate).
- **Workloads with bursty traffic** — DynamoDB on-demand mode scales automatically without capacity planning.
- **Multi-region deployments** — DynamoDB Global Tables provides active-active replication.

## When NOT to use it

- **Highest-throughput workloads** — PostgreSQL with SKIP LOCKED is still faster for batch claiming (one query vs N conditional updates).
- **Non-AWS environments** — DynamoDB is AWS-only (unless using a local emulator).
- **Cost-sensitive high-volume** — DynamoDB charges per request; at very high volumes, PostgreSQL on fixed compute is cheaper.
- **Deep history queries** — S3-backed history is slower than PostgreSQL for complex queries.

## Configuration

| Environment Variable | Required | Description |
|---------------------|----------|-------------|
| `DDB_TABLE` | yes | DynamoDB table name |
| `DDB_ENDPOINT` | no | Custom endpoint (for DynamoDB Local) |
| `S3_BUCKET` | yes | S3 bucket for history |
| `S3_ENDPOINT` | no | Custom S3 endpoint (for MinIO/rustfs) |
| `AWS_REGION` | yes | AWS region |

The store creates the table and GSI automatically via `CreateTable()`.

## Performance characteristics

| Operation | Typical latency | Scaling behavior |
|-----------|----------------|------------------|
| Enqueue | 5-10ms | Constant (conditional PutItem) |
| ClaimBatch (10 items) | 20-50ms | O(batch size) — GSI query + N conditional updates |
| Complete/Fail | 5-10ms | Constant (conditional UpdateItem) |
| CountByStatus | 10-30ms | O(items) for consistent count, O(1) for GSI count |
| List (100 items) | 10-30ms | O(limit) via GSI query |
| History query | 50-200ms | O(entries) — S3 list + read |

## Conformance

This backend passes the full 21-test conformance suite. Tested against:
- **DynamoDB Local** (amazon/dynamodb-local) + **rustfs** (S3-compatible) via Podman
