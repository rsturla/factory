# In-Memory Store Backend

The in-memory backend stores all state in Go data structures protected by a mutex. It is designed exclusively for **testing** — it is not durable and does not survive process restarts.

## How it works

- Work items are stored in a `map[itemKey]*WorkItem` keyed by `(queue, key)`.
- Queue configs are stored in a `map[string]*queueMeta`.
- History entries are appended to a `[]HistoryEntry` slice.
- Event subscribers receive from buffered channels; the store broadcasts on state changes.
- All operations are serialized via a single `sync.Mutex`.

### Claiming

Iterates the map to find pending items for the queue, sorts by priority/created_at, and mutates them in place. No contention handling needed — the mutex serializes everything.

### Events

`Subscribe` returns a buffered channel. State-changing operations (`Enqueue`, `Complete`, `Fail`, `Transition`, `Deadletter`) emit events to all subscribers for the affected queue. When the context is cancelled, the channel is closed and the subscriber is removed.

### Worker leases

Not tracked. `ListWorkers` always returns an empty list. Worker registration is irrelevant in test contexts.

## When to use it

- **Unit tests** — fast, deterministic, no external dependencies.
- **Conformance testing** — the in-memory store passes the full conformance suite, confirming that the test suite itself is correct.
- **Integration tests** — when you want to test the dispatcher, completion handler, or admin API without a database.
- **Development** — quick iteration without running PostgreSQL.

## When NOT to use it

- **Production** — no durability, no persistence, no concurrent access from multiple processes.
- **Load testing** — the mutex serializes all operations, so it won't reveal concurrency issues that appear with real backends.
- **Anything involving multiple processes** — state is local to one Go process.

## Configuration

No configuration needed. Create with `inmem.New()`.

```go
s := inmem.New()
s.EnsureQueue(ctx, "my-queue", store.QueueConfig{
    MaxConcurrency: 10,
    MaxRetry:       5,
})
```

## Performance characteristics

| Operation | Typical latency |
|-----------|----------------|
| Any operation | <1 microsecond |

All operations are O(n) in the number of items (map iteration), but with in-memory data structures this is effectively instant for test workloads.

## Conformance

This backend passes the full 21-test conformance suite and serves as the reference implementation for `store.Interface` behavior.
