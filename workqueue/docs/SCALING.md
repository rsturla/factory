# Scaling Guide

This document describes how Factory Workqueue scales from a single-node development setup to a production deployment handling 500,000+ jobs per day.

## Architecture recap

Each work type runs as an independent pipeline:

```
your service → receiver /enqueue → PostgreSQL → dispatcher → reconciler pods
```

- **Receiver**: stateless HTTP server, accepts enqueue requests, writes keys to the queue
- **Dispatcher**: active-active per queue (2+ replicas), claims items via SKIP LOCKED, invokes reconcilers
- **Reconciler**: stateless HTTP server, does the actual work (builds, tests, etc.)
- **PostgreSQL**: shared by all queues, handles all durable state

Each dispatcher replica claims batches of items using `SELECT FOR UPDATE SKIP LOCKED` to prevent double-claims. Max concurrency is enforced by counting rows in the `active_leases` table rather than maintaining a driftable counter. Multiple dispatcher replicas send concurrent HTTP requests to the reconciler Kubernetes Service, which load-balances across reconciler pods.

## What scales and what doesn't

| Component | Scaling model | Replicas |
|-----------|--------------|----------|
| Receiver | Horizontal (stateless) | 2-5, behind a Service |
| Dispatcher | Active-active per queue | 2-3 per queue |
| Reconciler | Horizontal via HPA | 2-200 per queue |
| PostgreSQL | Vertical + read replicas | 1 primary + 2 replicas (PGO) |
| Admin API | Horizontal (stateless) | 2-3, on read replica |

**The reconciler fleet is the primary scaling lever.** Dispatchers run active-active (no leader election) and use SKIP LOCKED + active_leases counting for safe concurrent claiming. All other components are lightweight enough that 2-3 replicas suffice (receiver, admin).

## Scaling the reconciler fleet

Reconciler pods are stateless HTTP servers. The dispatcher sends `POST /process` to the reconciler's Kubernetes Service, which distributes requests across all pods.

Scaling is handled externally by KEDA, Kubernetes HPA, or custom controllers. The dispatcher exposes Prometheus metrics on `/metrics` that provide the signals needed for scaling decisions.

### Prometheus metrics for scaling

The dispatcher exposes these metrics on its `/metrics` endpoint (default `:8080`):

| Metric | Type | Labels | Use for scaling |
|--------|------|--------|-----------------|
| `factory_queue_depth` | gauge | `queue`, `status` | Primary signal. Filter `status="pending"` for items waiting to be processed |
| `factory_in_progress` | gauge | `queue` | Current claimed + running items. Use to avoid over-scaling |
| `factory_max_concurrency` | gauge | `queue` | Upper bound — don't scale beyond this |
| `factory_oldest_pending_age_seconds` | gauge | `queue` | Scale-from-zero trigger. Non-zero means work is waiting |
| `factory_items_completed_total` | counter | `queue`, `outcome` | Processing rate via `rate()`. Use for capacity planning |
| `factory_items_enqueued_total` | counter | `queue` | Arrival rate via `rate()`. Predict scaling needs |
| `factory_reconcile_duration_seconds` | histogram | `queue`, `outcome` | Avg processing time. Informs items-per-pod ratio |

### KEDA ScaledObject

[KEDA](https://keda.sh) watches Prometheus metrics and manages the reconciler Deployment replica count:

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: rpm-update-reconciler
spec:
  scaleTargetRef:
    name: factory-rpm-update-reconciler
  minReplicaCount: 0
  maxReplicaCount: 100
  pollingInterval: 15
  cooldownPeriod: 300
  triggers:
    - type: prometheus
      metadata:
        serverAddress: http://prometheus.monitoring:9090
        query: factory_queue_depth{queue="rpm-update", status="pending"}
        threshold: "5"
        activationThreshold: "1"
```

This scales 1 replica per 5 pending items, activates from zero when any item is pending, and waits 5 minutes before scaling down.

### Kubernetes HPA

If you prefer native HPA with a Prometheus adapter:

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
          name: factory_queue_depth
          selector:
            matchLabels:
              queue: rpm-update
              status: pending
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

Requires [prometheus-adapter](https://github.com/kubernetes-sigs/prometheus-adapter) to expose `factory_queue_depth` as an external metric.

## Configuration tuning

### Dispatcher settings

| Parameter | Development | Production | Effect |
|-----------|-------------|------------|--------|
| `DISPATCH_INTERVAL` | 2s | 200ms | How often the dispatcher checks for work |
| `DISPATCH_BATCH_SIZE` | 5 | 100 | Items claimed per dispatch cycle |
| `DISPATCH_MAX_CONCURRENCY` | 5 | 500-2000 | Max items in-flight simultaneously |
| `DISPATCH_LEASE_DURATION` | 5m | 1h | How long before an uncompleted item is reclaimed |

**`DISPATCH_MAX_CONCURRENCY` is the most important setting.** It determines how many concurrent HTTP calls the dispatcher makes to the reconciler Service. Set it to the maximum number of reconciler pods you expect multiplied by the concurrency each pod can handle.

Example: 100 reconciler pods, each handling 10 concurrent requests = `DISPATCH_MAX_CONCURRENCY=1000`.

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

Plus 10-15 dispatchers (2-3 per queue), 5 receivers, 3 admin replicas, 3 PostgreSQL nodes = **~210 pods total**.

## Queue isolation

Each queue type is a completely independent pipeline. A burst of RPM rebuilds does not affect the codegen queue because:

1. Different dispatcher pods (independent active-active claim loops)
2. Different reconciler Deployments (independent HPA)
3. Different `max_concurrency` limits
4. Same PostgreSQL, but SKIP LOCKED queries are partitioned by `WHERE queue = $1`

To add a new work type, deploy a new 3-service stack (receiver + dispatcher + reconciler). No changes to existing queues.

## When you've outgrown this architecture

Signs that you need to evolve:

1. **Dispatchers can't claim fast enough**: dispatch cycle takes >100ms consistently. Fix: add more dispatcher replicas, reduce `DISPATCH_INTERVAL`, increase `DISPATCH_BATCH_SIZE`, or split into sub-queues.

2. **PostgreSQL write throughput saturated**: >5,000 txn/sec sustained. Fix: use CockroachDB (wire-compatible, auto-sharded) via the `store.Interface` abstraction.

3. **Queue depth exceeds millions**: listing and counting become slow. Fix: partition the `work_items` table by queue name, add completed item cleanup.

4. **Cross-region deployment needed**: Fix: DynamoDB Global Tables via the DynamoDB store backend, or CockroachDB multi-region.

For 500,000 jobs/day, none of these limits apply. The architecture handles 10x that before any of these become relevant.
