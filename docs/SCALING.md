# Scaling Guide

This document describes how Factory Workqueue scales from a single-node development setup to a production deployment handling 500,000+ jobs per day.

## Architecture recap

Each work type runs as an independent pipeline:

```
your service → receiver /enqueue → PostgreSQL → dispatcher → reconciler pods
```

- **Receiver**: stateless HTTP server, accepts enqueue requests, writes keys to the queue
- **Dispatcher**: singleton per queue, claims items via SKIP LOCKED, invokes reconcilers
- **Reconciler**: stateless HTTP server, does the actual work (builds, tests, etc.)
- **PostgreSQL**: shared by all queues, handles all durable state

The dispatcher claims batches of items and sends concurrent HTTP requests to the reconciler Kubernetes Service, which load-balances across reconciler pods.

## What scales and what doesn't

| Component | Scaling model | Replicas |
|-----------|--------------|----------|
| Receiver | Horizontal (stateless) | 2-5, behind a Service |
| Dispatcher | Singleton per queue | 1 per queue |
| Reconciler | Horizontal via HPA | 2-200 per queue |
| PostgreSQL | Vertical + read replicas | 1 primary + 2 replicas (PGO) |
| Admin API | Horizontal (stateless) | 2-3, on read replica |

**The reconciler fleet is the primary scaling lever.** Everything else is either a singleton (dispatcher) or lightweight enough that 2-3 replicas suffice (receiver, admin).

## Scaling the reconciler fleet

Reconciler pods are stateless HTTP servers. The dispatcher sends `POST /process` to the reconciler's Kubernetes Service, which distributes requests across all pods.

Use a Kubernetes HPA driven by queue depth:

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: rpm-update-reconciler
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: factory-rpm-update-reconciler
  minReplicas: 2
  maxReplicas: 100
  metrics:
    - type: External
      external:
        metric:
          name: factory_queue_depth_pending
          selector:
            matchLabels:
              queue: rpm-update
        target:
          type: AverageValue
          averageValue: "5"
  behavior:
    scaleUp:
      stabilizationWindowSeconds: 30
      policies:
        - type: Pods
          value: 10
          periodSeconds: 60
    scaleDown:
      stabilizationWindowSeconds: 300
```

This scales up when pending items exceed 5 per pod, and scales down conservatively (5-minute stabilization window) to avoid thrashing.

## Configuration tuning

### Dispatcher settings

| Parameter | Development | Production | Effect |
|-----------|-------------|------------|--------|
| `DISPATCH_INTERVAL` | 2s | 200ms | How often the dispatcher checks for work |
| `BATCH_SIZE` | 5 | 100 | Items claimed per dispatch cycle |
| `MAX_CONCURRENCY` | 5 | 500-2000 | Max items in-flight simultaneously |
| `LEASE_DURATION` | 5m | 1h | How long before an uncompleted item is reclaimed |

**`MAX_CONCURRENCY` is the most important setting.** It determines how many concurrent HTTP calls the dispatcher makes to the reconciler Service. Set it to the maximum number of reconciler pods you expect multiplied by the concurrency each pod can handle.

Example: 100 reconciler pods, each handling 10 concurrent requests = `MAX_CONCURRENCY=1000`.

### PostgreSQL tuning

| Parameter | Development | Production |
|-----------|-------------|------------|
| `max_connections` | 100 | 200+ |
| `shared_buffers` | 128MB | 25% of RAM |
| `work_mem` | 4MB | 64MB |
| `effective_cache_size` | 512MB | 75% of RAM |
| `maintenance_work_mem` | 64MB | 512MB |

Additionally:
- **Autovacuum**: the `work_items` table receives heavy UPDATE traffic (claim/complete cycles generate dead tuples). Tune autovacuum to run frequently:
  ```sql
  ALTER TABLE work_items SET (autovacuum_vacuum_scale_factor = 0.02);
  ALTER TABLE work_items SET (autovacuum_analyze_scale_factor = 0.01);
  ```
- **Connection pooling**: the Go `pgxpool` defaults to 4 connections. Set `pool_max_conns=50` in the connection string for production.

### Completed item cleanup

Completed items (`succeeded` status) accumulate in the `work_items` table. Without cleanup, the table grows unbounded. Run a periodic cleanup job:

```sql
DELETE FROM work_items
WHERE status IN ('succeeded', 'failed')
  AND completed_at < now() - interval '7 days';
```

Schedule this as a Kubernetes CronJob or a PostgreSQL `pg_cron` task.

## Capacity planning

### Throughput math

At 500,000 jobs/day:
- Average: ~6 jobs/sec
- Peak (10x burst): ~60 jobs/sec

Each job generates 3 PostgreSQL transactions: enqueue (INSERT), claim (UPDATE), complete (UPDATE). At peak: 180 txn/sec. A single PostgreSQL node handles 5,000+ txn/sec with SKIP LOCKED. **PostgreSQL is never the bottleneck.**

The bottleneck is reconcile time. If the average reconcile takes 30 seconds:

```
60 jobs/sec × 30 sec/job = 1,800 concurrent items
```

You need enough reconciler pods to handle 1,800 concurrent reconciliations. At 10 concurrent per pod, that's 180 pods across all queue types.

### Example: 500k jobs/day across 5 queue types

| Queue | Jobs/day | Avg reconcile time | Peak concurrent | Reconciler pods |
|-------|----------|-------------------|-----------------|-----------------|
| rpm-update | 200,000 | 60s | 800 | 80 |
| container-build | 100,000 | 120s | 800 | 80 |
| test-runner | 150,000 | 10s | 100 | 10 |
| codegen | 30,000 | 45s | 100 | 10 |
| mr-review | 20,000 | 5s | 15 | 5 |
| **Total** | **500,000** | | **1,815** | **185 pods** |

Plus 5 dispatchers (1 per queue), 5 receivers, 3 admin replicas, 3 PostgreSQL nodes = **~200 pods total**.

## Queue isolation

Each queue type is a completely independent pipeline. A burst of RPM rebuilds does not affect the codegen queue because:

1. Different dispatcher pods (independent claim loops)
2. Different reconciler Deployments (independent HPA)
3. Different `max_concurrency` limits
4. Same PostgreSQL, but SKIP LOCKED queries are partitioned by `WHERE queue = $1`

To add a new work type, deploy a new 3-service stack (receiver + dispatcher + reconciler). No changes to existing queues.

## When you've outgrown this architecture

Signs that you need to evolve:

1. **Single dispatcher can't claim fast enough**: dispatch cycle takes >100ms consistently. Fix: reduce `DISPATCH_INTERVAL`, increase `BATCH_SIZE`, or split into sub-queues.

2. **PostgreSQL write throughput saturated**: >5,000 txn/sec sustained. Fix: use CockroachDB (wire-compatible, auto-sharded) via the `store.Interface` abstraction.

3. **Queue depth exceeds millions**: listing and counting become slow. Fix: partition the `work_items` table by queue name, add completed item cleanup.

4. **Cross-region deployment needed**: Fix: DynamoDB Global Tables via the DynamoDB store backend, or CockroachDB multi-region.

For 500,000 jobs/day, none of these limits apply. The architecture handles 10x that before any of these become relevant.
