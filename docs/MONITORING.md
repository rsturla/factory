# Monitoring Guide

Factory V2 exposes Prometheus metrics, structured JSON logs, and a real-time event stream for operational visibility across all queues.

## Prometheus Metrics

All services expose metrics at `GET /metrics` in Prometheus exposition format.

### Counters

| Metric | Labels | Description |
|--------|--------|-------------|
| `factory_items_enqueued_total` | `queue` | Total items enqueued (including dedup merges) |
| `factory_items_dispatched_total` | `queue` | Total items claimed and sent to reconcilers |
| `factory_items_completed_total` | `queue`, `outcome` | Total items completed. Outcome: `succeeded`, `failed`, `dead_letter`, `converged` |
| `factory_items_reaped_total` | `queue` | Items reclaimed by the reaper (expired leases from dead workers) |
| `factory_items_deduped_total` | `queue` | Enqueue requests that hit an existing pending key |

### Gauges

| Metric | Labels | Description |
|--------|--------|-------------|
| `factory_queue_depth` | `queue`, `status` | Current item count by status (`pending`, `claimed`, `running`, `succeeded`, `failed`, `dead_letter`) |
| `factory_in_progress` | `queue` | Items currently being processed (claimed + running) |
| `factory_worker_count` | `queue`, `compute_backend`, `status` | Registered workers by backend and status |

### Histograms

| Metric | Labels | Buckets | Description |
|--------|--------|---------|-------------|
| `factory_claim_duration_seconds` | `queue` | Default | Time to execute a ClaimBatch query |
| `factory_reconcile_duration_seconds` | `queue`, `outcome` | 0.1 - 600s | Time the reconciler took to process an item |
| `factory_wait_latency_seconds` | `queue` | 0.1 - 3600s | Time an item spent pending before being claimed |
| `factory_e2e_latency_seconds` | `queue` | 1 - 7200s | Total time from enqueue to completion |
| `factory_attempts_at_completion` | `queue` | 1 - 20 | Number of attempts when an item completes |

## Key Metrics to Alert On

### Queue backlog growing

```promql
factory_queue_depth{status="pending"} > 1000
```

Pending items are accumulating faster than the dispatcher can process them. Either the reconciler fleet is too small (scale up via HPA) or the reconciler is slow/failing.

### Dead letter accumulation

```promql
rate(factory_items_completed_total{outcome="dead_letter"}[5m]) > 0
```

Items are exhausting retry budget and being dead-lettered. Check reconciler logs for the root cause — likely a persistent failure (bad config, external service down).

### Reaper activity

```promql
rate(factory_items_reaped_total[5m]) > 0
```

The reaper is reclaiming items from workers that disappeared without completing. Occasional reaping during deployments is normal. Sustained reaping indicates worker instability (OOM kills, node failures).

### High reconcile latency

```promql
histogram_quantile(0.99, rate(factory_reconcile_duration_seconds_bucket[5m])) > 300
```

P99 reconcile time exceeds 5 minutes. Reconciler pods may need more resources, or the workload changed.

### Claim latency spike

```promql
histogram_quantile(0.99, rate(factory_claim_duration_seconds_bucket[5m])) > 0.1
```

Claiming is taking >100ms. PostgreSQL may be under pressure (check connection pool, autovacuum, lock contention).

### Retry storm

```promql
histogram_quantile(0.5, factory_attempts_at_completion) > 3
```

Median items need 3+ attempts to complete. The reconciler is failing frequently — investigate the error pattern.

## Prometheus ServiceMonitor

For OpenShift/Kubernetes with Prometheus Operator:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: factory
  namespace: factory
spec:
  selector:
    matchLabels:
      app.kubernetes.io/part-of: factory
  endpoints:
    - port: http
      path: /metrics
      interval: 15s
```

## Grafana Dashboard

### Recommended panels

**Row 1: Overview**
- Queue depth by status (stacked bar, per queue)
- Items completed/sec by outcome (rate graph)
- In-progress gauge (per queue)

**Row 2: Latency**
- E2E latency heatmap (enqueue → complete)
- Wait latency heatmap (pending → claimed)
- Reconcile duration by outcome (P50, P95, P99)

**Row 3: Throughput**
- Enqueue rate vs dispatch rate (overlay)
- Claim batch duration (P99)
- Attempts at completion distribution

**Row 4: Health**
- Dead letter rate (should be ~0)
- Reaper activity (should be ~0 except during deploys)
- Worker count by backend

### Example Grafana panel JSON (queue depth)

```json
{
  "title": "Queue Depth",
  "type": "timeseries",
  "targets": [
    {
      "expr": "factory_queue_depth",
      "legendFormat": "{{queue}} / {{status}}"
    }
  ],
  "fieldConfig": {
    "defaults": {
      "custom": {
        "drawStyle": "bars",
        "stacking": { "mode": "normal" }
      }
    }
  }
}
```

## Distributed Tracing (OpenTelemetry)

All services emit OpenTelemetry traces when `OTEL_EXPORTER_OTLP_ENDPOINT` is set. Traces follow work items from enqueue through completion:

```
Trace (receiver):
└─ enqueue {queue: "echo", key: "curl.fc43", priority: 10}

Trace (dispatcher) ── linked to enqueue trace ──
└─ processItem {queue: "echo", key: "curl.fc43", attempt: 1}
   ├─ store.Transition (claimed → running)
   ├─ reconciler.Process (HTTP call, 30.2s)
   └─ outcome: completed, reconcile_duration_s: 30.2
```

The dispatcher's trace is **linked** to the receiver's trace via OTEL span links, enabling end-to-end visibility across the async queue boundary.

### Setup

Set on all services:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
OTEL_SERVICE_NAME=factory-dispatcher  # unique per service
```

When unset, tracing is noop — zero overhead.

### Trace ID correlation

- Trace IDs are stored in `work_item_history.trace_id` for post-hoc queries
- Trace IDs are included in structured log messages (`trace_id` field)
- The dispatcher passes `trace_id` in `ProcessRequest` to the reconciler, enabling reconciler authors to create child spans
- Click a log line in Grafana → jump to the trace in Jaeger/Tempo

### Local development

Use the tracing docker-compose with Jaeger:

```bash
cd deploy && docker compose -f docker-compose.tracing.yaml up --build -d

# Enqueue work
curl -X POST http://localhost:8081/enqueue -d '{"key":"test","priority":0}'

# View traces
open http://localhost:16686
# Search for service: factory-dispatcher
```

## Structured Logging

All services use Go's `log/slog` with JSON output. Log lines include trace IDs when tracing is enabled:

```json
{
  "time": "2026-03-30T22:51:30Z",
  "level": "INFO",
  "msg": "enqueued",
  "queue": "echo",
  "key": "curl.fc43",
  "priority": 10,
  "trace_id": "abc123def456..."
}
```

### Key log messages

| Level | Message | Meaning |
|-------|---------|---------|
| INFO | `claimed items` | Dispatcher claimed a batch. `count` field shows how many. |
| INFO | `dispatcher starting` | Dispatcher started. Shows `queue`, `worker_id`, `max_concurrency`. |
| WARN | `reconciler reported error` | Reconciler returned an error. Item will be retried. |
| WARN | `reaping expired item` | Reaper reclaiming an item with expired lease. |
| ERROR | `claim batch failed` | ClaimBatch query failed. Check PostgreSQL connectivity. |
| ERROR | `reconciler call failed` | HTTP call to reconciler failed (connection refused, timeout). Item requeued without consuming retry budget. |
| ERROR | `migration failed` | Schema migration failed on startup. |

### Log aggregation

Forward logs to your aggregation stack (Loki, Elasticsearch, CloudWatch) and filter by:
- `queue` — isolate logs for a specific work type
- `key` — trace a single work item through the system
- `level=ERROR` — surface failures

## Admin API

The admin API provides real-time operational queries without Prometheus:

```bash
# Queue overview
curl http://factory-admin:8080/admin/queues

# Items in a specific queue
curl http://factory-admin:8080/admin/queues/rpm-update/items?status=pending

# Full item detail with history
curl http://factory-admin:8080/admin/queues/rpm-update/items/curl-1.0-1.fc43

# Registered workers
curl http://factory-admin:8080/admin/workers
```

### Server-Sent Events

Stream real-time state changes for a queue:

```bash
curl -N http://factory-admin:8080/admin/queues/rpm-update/events
```

Each event is a JSON object:
```
data: {"queue":"rpm-update","key":"curl-1.0-1.fc43","status":"claimed","priority":10}
```

Use this for live dashboards or CLI tooling (`factoryctl events <queue>`).

## Health Checks

All services expose:

| Endpoint | Purpose | Use |
|----------|---------|-----|
| `GET /healthz` | Liveness probe | Kubernetes `livenessProbe` |
| `GET /readyz` | Readiness probe | Kubernetes `readinessProbe` |

Configure in Deployment manifests:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 3
  periodSeconds: 5
```
