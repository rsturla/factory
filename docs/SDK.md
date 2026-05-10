# Reconciler SDK

This guide covers everything you need to write a reconciler for Factory. The protocol is plain HTTP+JSON, so any language works. A Go SDK is provided for convenience.

## Wire protocol

The dispatcher POSTs a JSON request to your reconciler's `/process` endpoint and expects a JSON response.

### Request

```json
{
  "key": "curl-1.0-1.fc43",
  "attempt": 1,
  "priority": 10,
  "trace_id": "00-abc123-def456-01"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `key` | string | The work item key. Use this to look up state from your source of truth. |
| `attempt` | int | Current attempt number (starts at 1). |
| `priority` | int | Priority the item was enqueued with. |
| `trace_id` | string | Optional trace ID for distributed tracing. |

### Response

Return a JSON object with an `action` field:

| Action | Meaning | Example |
|--------|---------|---------|
| `"completed"` | Work done successfully | `{"action": "completed"}` |
| `"converged"` | Desired state already met, no work needed | `{"action": "converged"}` |
| `"requeue"` | Re-enqueue after a delay (does not consume retry budget) | `{"action": "requeue", "requeue_after": "30s"}` |
| `"fan_out"` | Complete this item and enqueue new items | `{"action": "fan_out", "fan_out_keys": ["a", "b"]}` |

To signal a retriable failure, set the `error` field:

```json
{"error": "upstream timeout"}
```

The dispatcher will requeue the item with exponential backoff, consuming one retry attempt.

### Response fields

| Field | Type | Description |
|-------|------|-------------|
| `action` | string | One of: `completed`, `converged`, `requeue`, `fan_out`. |
| `requeue_after` | string | Go duration string (e.g., `"30s"`, `"5m"`). Required when action is `requeue`. |
| `fan_out_keys` | []string | Keys to enqueue into the same queue. Required when action is `fan_out`. |
| `error` | string | Error message for retriable failures. |

## Choosing a pattern

Two patterns depending on how long the work takes and where it runs:

| Duration | Pattern | Dispatcher mode | Example |
|----------|---------|----------------|---------|
| Under 2 minutes | HTTP reconciler | `push` | MR review, test runner, API calls |
| 2-60 minutes | Either | Depends | Container build (K8s = push, EC2 = standalone) |
| Over 60 minutes | Standalone worker | `scale-only` | RPM build, AI inference |

For work that delegates to external systems (Koji, Tekton, CI) and polls for completion, use an HTTP reconciler with `requeue` regardless of how long the external work takes -- each invocation is a quick status check.

## Go SDK

Install the SDK module (zero external dependencies):

```bash
go get github.com/hummingbird-org/factory-workqueue/pkg/sdk
```

### HTTP reconciler (push model)

The dispatcher calls your `/process` endpoint. Set `DISPATCH_MODE=push` (default). See `examples/echo-reconciler/`.

```go
package main

import (
    "context"
    "net/http"

    "github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

func main() {
    mux := http.NewServeMux()
    mux.Handle("POST /process", sdk.ReconcilerHandler(reconcile))
    http.ListenAndServe(":8082", mux)
}

func reconcile(ctx context.Context, req sdk.ProcessRequest) (sdk.ProcessResponse, error) {
    if alreadyDone(ctx, req.Key) {
        return sdk.Converged(), nil
    }
    if err := doWork(ctx, req.Key); err != nil {
        return sdk.ProcessResponse{}, err
    }
    return sdk.Completed(), nil
}
```

### Response builders

```go
sdk.Completed()              // work done
sdk.Converged()              // already at desired state
sdk.RequeueAfter(30*time.Second) // check back later
sdk.FanOut("child-1", "child-2") // complete and spawn children
```

Returning a non-nil `error` from your `ReconcileFunc` signals a retriable failure.

### Cross-queue fan-out

The `fan_out` action enqueues keys into the **same queue**. To enqueue into a different queue, use `EnqueueClient`:

```go
client := sdk.NewEnqueueClient("http://other-receiver:8081")
err := client.Enqueue(ctx, "container-build", "myimage-1.0", 10)
```

### Standalone worker (pull model)

The worker claims items via the workqueue HTTP API, processes them locally, and reports back. Set `DISPATCH_MODE=scale-only` on the dispatcher. See `examples/standalone-worker/`.

```go
import "github.com/hummingbird-org/factory-workqueue/pkg/client"

wq := client.NewWorkqueueClient("http://factory-receiver:8081")

for {
    items, _ := wq.ClaimBatch(ctx, "rpm-update", 1, workerID, 2*time.Hour)
    if len(items) == 0 {
        time.Sleep(5 * time.Second)
        continue
    }

    // Heartbeat in background to keep lease alive
    go heartbeat(ctx, wq, item)

    // Do heavy local work (minutes/hours)
    if err := rpmbuild(item.Key); err != nil {
        wq.Fail(ctx, item.Queue, item.Key, err.Error())
    } else {
        wq.Complete(ctx, item.Queue, item.Key)
    }
}
```

Both patterns use the same store, same state machine, same retry/dead-letter logic. If a worker dies mid-work, the lease expires and the reaper reclaims the item. Workers exit after `MAX_IDLE` (default 10m) with no work, and the dispatcher scales compute back down.

## Non-Go reconcilers

Since the protocol is HTTP+JSON, you can write a reconciler in any language. Here are minimal examples.

### Python

```python
from flask import Flask, request, jsonify

app = Flask(__name__)

@app.post("/process")
def process():
    req = request.json
    key = req["key"]

    # Look up state from your source of truth, do work...

    return jsonify({"action": "completed"})
```

### Shell (for prototyping)

```bash
#!/bin/bash
# Minimal reconciler using ncat (from nmap)
while true; do
  ncat -lk -p 8082 -e /bin/sh -c '
    read -r method path version
    while IFS= read -r header; do [ "$header" = $'"'"'\r'"'"' ] && break; done
    read -r body
    key=$(echo "$body" | jq -r .key)
    echo -ne "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
    echo "{\"action\":\"completed\"}"
  '
done
```

## Lifecycle

Understanding how the dispatcher manages your item:

1. **Enqueue** -- a key is added to the queue via the receiver.
2. **Claim** -- the dispatcher claims the item with a lease (default 1h).
3. **Process** -- the dispatcher POSTs to your reconciler.
4. **Outcome** -- based on your response:
   - `completed`/`converged` -- item marked done.
   - `requeue` -- item re-enqueued after the specified delay (no retry budget cost).
   - `fan_out` -- item marked done, child keys enqueued.
   - `error` -- item requeued with exponential backoff (consumes retry budget).
5. **Dead-letter** -- after `DISPATCH_MAX_RETRY` failures (default 5), the item is dead-lettered.
6. **Reaper** -- if a worker dies and the lease expires, the reaper reclaims the item.

## Tips

- **Keys, not payloads.** The queue only stores keys. Your reconciler should fetch current state from its own source of truth (Git, a registry, an API). This makes reconciliation naturally idempotent.
- **Idempotency.** Your reconciler may be called more than once for the same key (retries, lease expiry). Use `Converged` when there is nothing to do.
- **Requeue for polling.** When delegating to external systems (CI, build services), return `requeue` with a short delay to poll for completion. Each invocation is a quick status check.
- **Fan-out for decomposition.** Use `fan_out` to break a large task into smaller units. The parent completes and children are processed independently.
