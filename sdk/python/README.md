# Factory Workqueue Python SDK

Python SDK for the [Factory Workqueue](../../README.md) platform. Provides two usage patterns:

1. **Reconciler SDK** — Build reconcilers that the dispatcher calls via HTTP
2. **Workqueue Client** — Standalone workers that claim and process items directly

## Install

```bash
pip install "factory-workqueue @ git+https://github.com/hummingbird-org/factory-workqueue.git#subdirectory=sdk/python"
```

## Reconciler Pattern

The dispatcher calls your reconciler's `/process` endpoint with a key. You fetch state, do work, and return a response.

```python
from factory_workqueue import ReconcilerHandler, ProcessRequest, completed, requeue_after, serve
from datetime import timedelta

def reconcile(req: ProcessRequest):
    print(f"Processing {req.key} (attempt {req.attempt})")

    # Do work...
    success = build_package(req.key)

    if success:
        return completed()
    else:
        return requeue_after(timedelta(minutes=5))

# Serve on :8082 (requires uvicorn)
serve(reconcile)
```

Response builders:

| Builder | Action | Effect |
|---------|--------|--------|
| `completed()` | completed | Item marked succeeded |
| `converged()` | converged | Already at desired state, no work done |
| `requeue_after(delay)` | requeue | Re-enqueue after delay (no retry budget cost) |
| `fan_out("a", "b")` | fan_out | Complete parent, enqueue children |

Raising an exception signals a retriable failure (consumes retry budget).

## Standalone Worker Pattern

For `scale-only` mode where workers claim items directly.

```python
from factory_workqueue import WorkqueueClient, Status
from datetime import timedelta

with WorkqueueClient("http://receiver:8081") as client:
    while True:
        items = client.claim_batch("builds", batch_size=5, worker_id="worker-1",
                                   lease_duration=timedelta(hours=1))
        for item in items:
            try:
                build(item.key)
                client.complete("builds", item.key)
            except Exception as e:
                client.fail("builds", item.key, str(e))
```

### Async variant

```python
from factory_workqueue import AsyncWorkqueueClient
from datetime import timedelta

async with AsyncWorkqueueClient("http://receiver:8081") as client:
    items = await client.claim_batch("builds", 5, "worker-1", timedelta(hours=1))
    for item in items:
        await client.complete("builds", item.key)
```

## Cross-Queue Enqueue

Reconcilers can trigger work in other queues:

```python
from factory_workqueue import EnqueueClient

with EnqueueClient("http://other-receiver:8081") as client:
    client.enqueue("container-builds", "myimage:latest", priority=10)
```

## API Reference

### Types

- `Status` — Enum: `pending`, `claimed`, `running`, `succeeded`, `failed`, `dead_letter`
- `WorkItem` — Full work item with all fields
- `QueueConfig` — Queue configuration (max_concurrency, max_retry, compute_backend)
- `QueueInfo` — Queue info with status counts
- `ListFilter` — Filter for listing items
- `HistoryEntry` — State transition record
- `BatchEnqueueItem` — Item for batch enqueue

### Errors

- `WorkqueueError` — Base exception
- `APIError` — Non-2xx HTTP response (has `status_code` and `body`)
- `NotFoundError` — 404
- `ConflictError` — 409
- `InvalidRequestError` — 400

### Client Methods

All methods available on both `WorkqueueClient` (sync) and `AsyncWorkqueueClient` (async):

`enqueue`, `enqueue_batch`, `claim_batch`, `complete`, `fail`, `requeue`, `deadletter`, `extend_lease`, `transition`, `ensure_queue`, `repair_counter`, `set_queue_paused`, `is_queue_paused`, `count_by_status`, `list`, `get_item`, `list_queues`, `list_workers`, `purge_dead_letters`, `list_expired_leases`, `record_history`, `get_item_history`, `ping`
