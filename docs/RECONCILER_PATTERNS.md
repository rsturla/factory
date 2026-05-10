# Reconciler Design Patterns

This guide covers proven patterns for building reconcilers. Each pattern includes when to use it, how it works, and a complete Go example.

## Core principle: Reconcile, don't process

A reconciler is not a message consumer. It doesn't process a payload — it **reconciles** a key to its desired state. Every reconciler follows the same structure:

```
1. Receive key
2. Fetch current state from source of truth
3. Compare to desired state
4. If different: act, then report completed
5. If same: report converged
```

This means your reconciler must be **idempotent**. It will be called multiple times for the same key — on retries, after lease expiry, after re-enqueue. Each call must produce the same result regardless of how many times it runs.

## Pattern 1: Direct Work

**When:** The reconciler does the work itself. Work completes within the lease duration (default 1 hour).

**Examples:** Apply a Kubernetes manifest, update a DNS record, send a notification, run a linter.

```go
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    // 1. Fetch desired state.
    manifest, err := gitRepo.GetManifest(ctx, req.Key)
    if err != nil {
        return reconciler.ProcessResponse{}, fmt.Errorf("fetch manifest: %w", err)
    }

    // 2. Fetch current state.
    current, err := k8s.GetDeployment(ctx, manifest.Name)
    if err != nil && !errors.Is(err, ErrNotFound) {
        return reconciler.ProcessResponse{}, fmt.Errorf("get deployment: %w", err)
    }

    // 3. Compare.
    if current != nil && current.Spec == manifest.Spec {
        return reconciler.Converged(), nil
    }

    // 4. Act.
    if err := k8s.Apply(ctx, manifest); err != nil {
        return reconciler.ProcessResponse{}, err
    }

    return reconciler.Completed(), nil
}
```

**Key properties:**
- Work happens synchronously in the HTTP handler
- Errors return `(ProcessResponse{}, err)` — the dispatcher retries with backoff
- `Converged()` short-circuits when there's nothing to do
- The `ctx` carries the lease deadline — respect it

## Pattern 2: Delegated Polling

**When:** The reconciler delegates work to an external system and polls for completion. The external work takes minutes to hours.

**Examples:** Trigger a Koji RPM build, start a Tekton pipeline, submit a CI job, request an AI code review.

```go
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    // 1. Check if work is already running.
    build, err := koji.GetBuild(ctx, req.Key)
    if err != nil {
        return reconciler.ProcessResponse{}, err
    }

    switch {
    case build == nil:
        // 2a. No build exists — start one.
        if err := koji.StartBuild(ctx, req.Key); err != nil {
            return reconciler.ProcessResponse{}, err
        }
        // Check back in 30 seconds.
        return reconciler.RequeueAfter(30 * time.Second), nil

    case build.Status == "running":
        // 2b. Build is running — check again later.
        return reconciler.RequeueAfter(30 * time.Second), nil

    case build.Status == "succeeded":
        // 2c. Build finished.
        return reconciler.Completed(), nil

    case build.Status == "failed":
        // 2d. Build failed — report error for retry with backoff.
        return reconciler.ProcessResponse{}, fmt.Errorf("build failed: %s", build.Error)

    default:
        return reconciler.RequeueAfter(1 * time.Minute), nil
    }
}
```

**Key properties:**
- Each invocation is a quick status check (milliseconds), not long-running work
- `RequeueAfter` does NOT consume retry budget — it's an intentional delay
- Returning `error` DOES consume retry budget — use it for real failures
- The reconciler is stateless — it re-derives everything from the external system
- Works with `DISPATCH_MODE=push` even for hour-long builds

**Polling interval guidance:**
- 10-30s for builds/CI (want fast feedback)
- 1-5m for slow external systems (don't hammer APIs)
- Use adaptive intervals: short at first, longer as time passes

## Pattern 3: Fan-Out / Decomposition

**When:** A single key represents work that should be broken into independent sub-tasks.

**Examples:** A package update triggers multiple arch builds. A release triggers container builds for 10 images. A test suite fans out to per-test-file items.

```go
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    // req.Key = "curl-8.7.1-2.fc43"
    pkg, err := registry.GetPackage(ctx, req.Key)
    if err != nil {
        return reconciler.ProcessResponse{}, err
    }

    // Fan out to per-architecture builds.
    var children []string
    for _, arch := range pkg.Architectures {
        children = append(children, fmt.Sprintf("%s.%s", req.Key, arch))
        // e.g., "curl-8.7.1-2.fc43.x86_64", "curl-8.7.1-2.fc43.aarch64"
    }

    return reconciler.FanOut(children...), nil
}
```

**Key properties:**
- Parent item completes immediately, children are enqueued independently
- Children inherit the parent's priority
- Children can themselves fan out (tree decomposition)
- Fan-out keys go into the **same queue** — use `EnqueueClient` for cross-queue

**Cross-queue fan-out:**

```go
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    // RPM build succeeded — trigger container rebuild.
    containerReceiver := reconciler.NewEnqueueClient("http://container-receiver:8081")
    err := containerReceiver.Enqueue(ctx, "base-image-rebuild", 10)
    if err != nil {
        return reconciler.ProcessResponse{}, err
    }
    return reconciler.Completed(), nil
}
```

## Pattern 4: Convergence Loop

**When:** The desired state requires multiple steps that must happen in order, and each step's readiness depends on external conditions.

**Examples:** Promote a package through testing → staging → production. Build RPM → build container → push to registry → update deployment.

```go
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    release, err := db.GetRelease(ctx, req.Key)
    if err != nil {
        return reconciler.ProcessResponse{}, err
    }

    switch release.Phase {
    case "building":
        if !release.BuildComplete {
            return reconciler.RequeueAfter(30 * time.Second), nil
        }
        release.Phase = "testing"
        db.UpdateRelease(ctx, release)
        return reconciler.RequeueAfter(0), nil

    case "testing":
        results, _ := ci.GetTestResults(ctx, release.TestRunID)
        if results == nil {
            // Tests not started yet.
            ci.StartTests(ctx, release)
            return reconciler.RequeueAfter(1 * time.Minute), nil
        }
        if results.Status == "running" {
            return reconciler.RequeueAfter(30 * time.Second), nil
        }
        if results.Status == "failed" {
            return reconciler.ProcessResponse{}, fmt.Errorf("tests failed: %d failures", results.Failures)
        }
        release.Phase = "promoting"
        db.UpdateRelease(ctx, release)
        return reconciler.RequeueAfter(0), nil

    case "promoting":
        if err := registry.Promote(ctx, release); err != nil {
            return reconciler.ProcessResponse{}, err
        }
        return reconciler.Completed(), nil

    default:
        return reconciler.Converged(), nil
    }
}
```

**Key properties:**
- State machine lives in your source of truth, not in the queue
- Each invocation advances at most one step
- `RequeueAfter(0)` immediately re-enqueues for the next step
- If the reconciler crashes mid-step, the next invocation re-evaluates from current state
- No distributed transactions needed — each step is independently idempotent

## Pattern 5: Standalone Worker (Pull Model)

**When:** Work requires local resources (GPU, large disk, specific hardware), takes hours, or needs a persistent process.

**Examples:** RPM builds on dedicated build hosts, AI model inference on GPU nodes, large-scale data processing.

```go
func main() {
    wq := client.NewWorkqueueClient("http://factory-receiver:8081")
    workerID := hostname()

    for {
        items, err := wq.ClaimBatch(ctx, "rpm-build", 1, workerID, 2*time.Hour)
        if err != nil || len(items) == 0 {
            time.Sleep(5 * time.Second)
            continue
        }
        item := items[0]

        // Keep lease alive in background.
        hctx, hcancel := context.WithCancel(ctx)
        go func() {
            ticker := time.NewTicker(10 * time.Minute)
            defer ticker.Stop()
            for {
                select {
                case <-hctx.Done():
                    return
                case <-ticker.C:
                    wq.ExtendLease(ctx, item.Queue, item.Key, 2*time.Hour)
                }
            }
        }()

        // Do heavy work.
        err = rpmbuild(item.Key)
        hcancel() // Stop heartbeat.

        if err != nil {
            wq.Fail(ctx, item.Queue, item.Key, err.Error())
        } else {
            wq.Complete(ctx, item.Queue, item.Key)
        }
    }
}
```

**Key properties:**
- Worker pulls work, not pushed by dispatcher
- Set `DISPATCH_MODE=scale-only` on the dispatcher — it manages scaling and reaping but doesn't dispatch
- Heartbeat extends the lease — without it, the reaper reclaims the item after lease expiry
- Worker exits after idle timeout, dispatcher scales compute down

## Anti-patterns

### Don't carry state in the key

```
BAD:  "build:curl:8.7.1:x86_64:retry=3:priority=high"
GOOD: "curl-8.7.1-2.fc43.x86_64"
```

The key is an identifier, not a serialized message. State belongs in your source of truth.

### Don't ignore Converged

```go
// BAD: always does work even if already done
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    doWork(ctx, req.Key) // runs every time, even on retry
    return reconciler.Completed(), nil
}

// GOOD: checks first
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    if isDone(ctx, req.Key) {
        return reconciler.Converged(), nil
    }
    doWork(ctx, req.Key)
    return reconciler.Completed(), nil
}
```

Without the convergence check, retries and lease-expiry re-deliveries redo completed work.

### Don't use error for expected delays

```go
// BAD: reports error when build is just slow
if build.Status == "running" {
    return reconciler.ProcessResponse{}, fmt.Errorf("build still running")
}

// GOOD: requeue without consuming retry budget
if build.Status == "running" {
    return reconciler.RequeueAfter(30 * time.Second), nil
}
```

Errors consume retry budget. After `DISPATCH_MAX_RETRY` errors, the item is dead-lettered. Use `RequeueAfter` for expected waits.

### Don't hold long-running work in an HTTP reconciler

```go
// BAD: blocks HTTP handler for 30 minutes
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    result := runBuild(ctx, req.Key) // 30 minutes
    return reconciler.Completed(), nil
}

// GOOD: delegate and poll
func reconcile(ctx context.Context, req reconciler.ProcessRequest) (reconciler.ProcessResponse, error) {
    build := getBuild(ctx, req.Key)
    if build == nil {
        startBuild(ctx, req.Key)
        return reconciler.RequeueAfter(30 * time.Second), nil
    }
    if build.Running {
        return reconciler.RequeueAfter(30 * time.Second), nil
    }
    return reconciler.Completed(), nil
}
```

HTTP reconcilers should return quickly. For long work, use the delegated polling pattern or a standalone worker.

## Testing reconcilers

Reconcilers are plain HTTP handlers. Test them with `httptest`:

```go
func TestReconciler(t *testing.T) {
    handler := reconciler.ReconcilerHandler(reconcile)
    srv := httptest.NewServer(handler)
    defer srv.Close()

    body := `{"key": "test-item", "attempt": 1}`
    resp, _ := http.Post(srv.URL+"/process", "application/json", strings.NewReader(body))

    var result reconciler.ProcessResponse
    json.NewDecoder(resp.Body).Decode(&result)

    if result.Action != reconciler.ActionCompleted {
        t.Errorf("expected completed, got %s", result.Action)
    }
}
```

For integration testing, use the inmem store to run the full dispatcher → reconciler loop without external dependencies. See `tests/integration/platform_test.go` for examples.
