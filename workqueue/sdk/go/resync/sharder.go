// Package resync shards periodic reconciliation across cron firings,
// enqueuing only the slice of keys that belong in the current tick window.
//
// The model:
//
//   - A cron job fires every Tick (e.g. every hour). Tick is also the shard
//     size and the minimum Period.
//   - Each key set has a Period: how often its keys must be reconciled.
//     Period must be a positive multiple of Tick.
//   - On every tick, each key is deterministically assigned a second within
//     [0, Period) via FNV-1a. Keys whose assigned second falls in the current
//     tick window are enqueued with absolute NotBefore timestamps spread
//     across the window.
//
// Across one Period every key is enqueued exactly once. The assignment
// reshuffles between periods so the order changes from cycle to cycle.
// Different queue names produce decorrelated assignments, preventing
// thundering-herd effects on shared downstream systems.
//
// A caller constructs one [Sharder] at startup, then calls [Sharder.Process]
// on each cron tick:
//
//	sh, err := resync.New("repo-scan", time.Hour, wq)
//	if err != nil { return err }
//
//	// Each cron tick:
//	result, err := sh.Process(ctx, 24*time.Hour, keys)
//	log.Printf("enqueued %d/%d keys in %s", result.Enqueued, result.InShard, result.Elapsed)
//
// Period boundaries are aligned to the Go zero time (time.Time{}), not to
// calendar boundaries. For 24h periods this gives midnight UTC.
package resync

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/hummingbird-org/factory-workqueue/pkg/types"
)

// maxBatchSize matches the factory receiver's server-side limit
// (cmd/receiver/main.go).
const maxBatchSize = 10_000

// Enqueuer is the subset of factory-workqueue needed by the Sharder.
// Both [client.WorkqueueClient] and the internal store implementations
// satisfy this interface.
type Enqueuer interface {
	EnqueueBatch(ctx context.Context, queue string, items []types.BatchEnqueueItem) (int, error)
}

// Result reports what the Sharder did during a single [Sharder.Process] call.
type Result struct {
	// InShard is the number of keys whose hash fell into the current tick's shard.
	InShard int
	// Enqueued is the number of items the store accepted (may differ from
	// InShard due to in-flight deduplication).
	Enqueued int
	// Elapsed is how long Process took.
	Elapsed time.Duration
	// Overrun is true if Elapsed exceeded the tick duration.
	Overrun bool
}

// Option configures a [Sharder].
type Option func(*options)

type options struct {
	now      func() time.Time
	priority func(key string) int
}

// WithNow overrides the clock used by [Sharder.Process] and [Sharder.Preview].
// The function must be safe for concurrent use. Default is [time.Now].
func WithNow(fn func() time.Time) Option {
	return func(o *options) { o.now = fn }
}

// WithPriority sets a function that returns the priority for a given key.
// Higher values are claimed first by factory's dispatcher.
// Default is 0 for all keys.
func WithPriority(fn func(key string) int) Option {
	return func(o *options) { o.priority = fn }
}

// Sharder deterministically assigns keys to time slots within a period and
// enqueues only the keys belonging to the current slot. It is safe for
// concurrent use and reusable across cron firings.
type Sharder struct {
	queue    string
	tick     time.Duration
	enqueuer Enqueuer
	opts     options
}

// New creates a Sharder for the named queue.
//
//   - queue identifies the factory-workqueue queue to enqueue into.
//   - tick is the cron period (how often the job fires). Must be ≥ 1s.
//   - enqueuer receives the batched items.
func New(queue string, tick time.Duration, enqueuer Enqueuer, opts ...Option) (*Sharder, error) {
	if queue == "" {
		return nil, fmt.Errorf("resync: queue name must not be empty")
	}
	if tick < time.Second {
		return nil, fmt.Errorf("resync: tick must be ≥ 1s, got %s", tick)
	}
	if tick%time.Second != 0 {
		return nil, fmt.Errorf("resync: tick must be a whole number of seconds, got %s", tick)
	}
	if enqueuer == nil {
		return nil, fmt.Errorf("resync: enqueuer must not be nil")
	}

	o := options{
		now:      time.Now,
		priority: func(string) int { return 0 },
	}
	for _, opt := range opts {
		opt(&o)
	}

	return &Sharder{
		queue:    queue,
		tick:     tick,
		enqueuer: enqueuer,
		opts:     o,
	}, nil
}

// Process computes which keys belong to the current tick's shard and enqueues
// them. period is the total reconciliation cycle (e.g. 24h) and must be a
// positive multiple of the tick duration.
//
// Each key is assigned an absolute NotBefore timestamp within the current tick
// window. If the sharder runs late within the tick, past-due timestamps are
// immediately eligible — factory's dispatcher handles this naturally.
//
// If the shard contains more than 10,000 keys, Process splits them into
// multiple EnqueueBatch calls.
func (s *Sharder) Process(ctx context.Context, period time.Duration, keys []string) (Result, error) {
	now := s.opts.now
	start := now()

	items, err := s.shard(period, keys)
	if err != nil {
		return Result{}, err
	}

	result := Result{InShard: len(items)}

	for i := 0; i < len(items); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(items) {
			end = len(items)
		}
		n, err := s.enqueuer.EnqueueBatch(ctx, s.queue, items[i:end])
		if err != nil {
			result.Elapsed = now().Sub(start)
			result.Overrun = result.Elapsed > s.tick
			return result, fmt.Errorf("resync: enqueue batch [%d:%d]: %w", i, end, err)
		}
		result.Enqueued += n
	}

	result.Elapsed = now().Sub(start)
	result.Overrun = result.Elapsed > s.tick
	return result, nil
}

// Preview returns the items that [Sharder.Process] would enqueue for the
// current tick without actually enqueuing them. Useful for debugging and
// dry-runs.
func (s *Sharder) Preview(_ context.Context, period time.Duration, keys []string) ([]types.BatchEnqueueItem, error) {
	return s.shard(period, keys)
}

// shard is the shared logic for Process and Preview.
func (s *Sharder) shard(period time.Duration, keys []string) ([]types.BatchEnqueueItem, error) {
	if period <= 0 {
		return nil, fmt.Errorf("resync: period must be positive, got %s", period)
	}
	if period%s.tick != 0 {
		return nil, fmt.Errorf("resync: period (%s) must be a multiple of tick (%s)", period, s.tick)
	}

	if len(keys) == 0 {
		return nil, nil
	}

	now := s.opts.now()
	periodStart := now.Truncate(period)
	tickStart := now.Truncate(s.tick)

	periodSeconds := uint64(period / time.Second)
	tickSeconds := uint64(s.tick / time.Second)
	shardStartSec := uint64(tickStart.Sub(periodStart) / time.Second)

	salt := computeSalt(s.queue, periodStart)

	var items []types.BatchEnqueueItem
	for _, key := range keys {
		keySec := bucket(salt, key, periodSeconds)
		if keySec < shardStartSec || keySec >= shardStartSec+tickSeconds {
			continue
		}
		nb := tickStart.Add(time.Duration(keySec-shardStartSec) * time.Second)
		items = append(items, types.BatchEnqueueItem{
			Key:       key,
			Priority:  s.opts.priority(key),
			NotBefore: &nb,
		})
	}

	return items, nil
}

// computeSalt builds a per-period, per-queue salt. The null byte prevents
// prefix collisions between queue names.
func computeSalt(queue string, periodStart time.Time) []byte {
	buf := make([]byte, 0, len(queue)+1+8)
	buf = append(buf, queue...)
	buf = append(buf, 0)
	buf = binary.BigEndian.AppendUint64(buf, uint64(periodStart.Unix()))
	return buf
}

// bucket assigns key to a second within [0, periodSeconds) using FNV-1a.
func bucket(salt []byte, key string, periodSeconds uint64) uint64 {
	h := fnv.New64a()
	h.Write(salt)
	h.Write([]byte(key))
	return h.Sum64() % periodSeconds
}
