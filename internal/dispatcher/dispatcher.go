// Package dispatcher implements the core dispatch engine for the factory work queue.
//
// The dispatcher claims items from a queue via SELECT FOR UPDATE SKIP LOCKED,
// invokes the reconciler service over HTTP, and manages the lifecycle of
// work items (leases, retries, dead-lettering, reaping).
//
// It runs four concurrent loops:
//   - Dispatch: claim items and invoke the reconciler
//   - Sweep: repair counters, reschedule delayed items, update metric gauges
//   - Reaper: reclaim items with expired leases
//   - Scale: adjust worker count via the compute provider
package dispatcher

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/hummingbird-org/factory/internal/completion"
	"github.com/hummingbird-org/factory/internal/compute"
	"github.com/hummingbird-org/factory/internal/metrics"
	"github.com/hummingbird-org/factory/internal/workqueue"
	"github.com/hummingbird-org/factory/pkg/client"
	"github.com/hummingbird-org/factory/pkg/sdk"
)

// Dispatcher manages the lifecycle of work items for a single queue.
type Dispatcher struct {
	wq         workqueue.Interface
	reconciler *client.ReconcilerClient
	compute    compute.Provider
	completion *completion.Handler
	cfg        Config

	// inFlight tracks items currently being processed.
	inFlight   sync.WaitGroup
}

// New creates a new Dispatcher.
func New(wq workqueue.Interface, reconciler *client.ReconcilerClient, cp compute.Provider, cfg Config) *Dispatcher {
	compCfg := completion.Config{
		MaxAttempts:    cfg.MaxRetry,
		BackoffBase:    30 * time.Second,
		BackoffMax:     10 * time.Minute,
		JitterFraction: 0.25,
	}

	return &Dispatcher{
		wq:         wq,
		reconciler: reconciler,
		compute:    cp,
		completion: completion.NewHandler(wq, compCfg),
		cfg:        cfg,
	}
}

// Run starts the dispatcher loops and blocks until the context is cancelled.
// On cancellation it drains in-flight work before returning.
func (d *Dispatcher) Run(ctx context.Context) error {
	// Ensure the queue exists in queue_state.
	if err := d.wq.EnsureQueue(ctx, d.cfg.QueueName, workqueue.QueueConfig{
		MaxConcurrency: d.cfg.MaxConcurrency,
		MaxRetry:       d.cfg.MaxRetry,
		ComputeBackend: d.compute.Name(),
	}); err != nil {
		return err
	}

	slog.Info("dispatcher starting",
		"queue", d.cfg.QueueName,
		"worker_id", d.cfg.WorkerID,
		"max_concurrency", d.cfg.MaxConcurrency,
		"dispatch_interval", d.cfg.DispatchInterval,
	)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error { return d.loop(gctx, "dispatch", d.cfg.DispatchInterval, d.dispatchTick) })
	g.Go(func() error { return d.loop(gctx, "sweep", d.cfg.SweepInterval, d.sweepTick) })
	g.Go(func() error { return d.loop(gctx, "reaper", d.cfg.ReaperInterval, d.reaperTick) })
	g.Go(func() error { return d.loop(gctx, "scale", d.cfg.ScaleInterval, d.scaleTick) })

	err := g.Wait()

	// Drain in-flight work.
	slog.Info("draining in-flight work", "queue", d.cfg.QueueName)
	d.inFlight.Wait()
	slog.Info("dispatcher stopped", "queue", d.cfg.QueueName)

	return err
}

// loop runs a tick function at the given interval until the context is cancelled.
func (d *Dispatcher) loop(ctx context.Context, name string, interval time.Duration, tick func(context.Context)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run immediately on start.
	tick(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			tick(ctx)
		}
	}
}

// dispatchTick claims a batch of items and invokes the reconciler for each.
func (d *Dispatcher) dispatchTick(ctx context.Context) {
	start := time.Now()
	items, err := d.wq.ClaimBatch(ctx, d.cfg.QueueName, d.cfg.BatchSize, d.cfg.WorkerID, d.cfg.LeaseDuration)
	if err != nil {
		slog.Error("claim batch failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	claimDur := time.Since(start).Seconds()
	metrics.ClaimDuration.WithLabelValues(d.cfg.QueueName).Observe(claimDur)

	if len(items) == 0 {
		return
	}

	slog.Info("claimed items", "queue", d.cfg.QueueName, "count", len(items))
	metrics.ItemsDispatched.WithLabelValues(d.cfg.QueueName).Add(float64(len(items)))

	for _, item := range items {
		d.inFlight.Add(1)
		go d.processItem(ctx, item)
	}
}

// processItem transitions an item to running, calls the reconciler, and handles the result.
func (d *Dispatcher) processItem(ctx context.Context, item workqueue.WorkItem) {
	defer d.inFlight.Done()

	// Transition claimed → running.
	if err := d.wq.Transition(ctx, item.Queue, item.Key, workqueue.StatusClaimed, workqueue.StatusRunning,
		workqueue.WithWorkerID(d.cfg.WorkerID)); err != nil {
		slog.Error("transition to running failed", "queue", item.Queue, "key", item.Key, "error", err)
		return
	}

	// Record wait latency (time in pending).
	if item.ClaimedAt != nil {
		waitSec := item.ClaimedAt.Sub(item.CreatedAt).Seconds()
		metrics.WaitLatency.WithLabelValues(item.Queue).Observe(waitSec)
	}

	// Call the reconciler.
	start := time.Now()
	resp, err := d.reconciler.Process(ctx, sdk.ProcessRequest{
		Key:      item.Key,
		Attempt:  item.Attempts,
		Priority: item.Priority,
	})
	reconcileDur := time.Since(start).Seconds()

	if err != nil {
		// Infrastructure failure — reconciler unreachable. Don't consume retry budget.
		slog.Error("reconciler call failed", "queue", item.Queue, "key", item.Key, "error", err)
		metrics.ReconcileDuration.WithLabelValues(item.Queue, "infra_error").Observe(reconcileDur)
		if infraErr := d.completion.HandleInfraFailure(ctx, item.Queue, item.Key); infraErr != nil {
			slog.Error("handle infra failure failed", "queue", item.Queue, "key", item.Key, "error", infraErr)
		}
		return
	}

	// Handle reconciler response.
	d.handleResponse(ctx, item, resp, reconcileDur)
}

func (d *Dispatcher) handleResponse(ctx context.Context, item workqueue.WorkItem, resp sdk.ProcessResponse, durSec float64) {
	queue := item.Queue
	key := item.Key

	// Check for reconciler-reported error (retriable failure).
	if resp.Error != "" {
		slog.Warn("reconciler reported error", "queue", queue, "key", key, "error", resp.Error)
		metrics.ReconcileDuration.WithLabelValues(queue, "failed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "failed").Inc()
		if err := d.completion.HandleFailure(ctx, queue, key, item.Attempts, resp.Error); err != nil {
			slog.Error("handle failure failed", "queue", queue, "key", key, "error", err)
		}
		return
	}

	switch resp.Action {
	case sdk.ActionCompleted:
		metrics.ReconcileDuration.WithLabelValues(queue, "completed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		if err := d.completion.HandleSuccess(ctx, queue, key); err != nil {
			slog.Error("handle success failed", "queue", queue, "key", key, "error", err)
		}

	case sdk.ActionConverged:
		metrics.ReconcileDuration.WithLabelValues(queue, "converged").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "converged").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		if err := d.completion.HandleSuccess(ctx, queue, key); err != nil {
			slog.Error("handle converged failed", "queue", queue, "key", key, "error", err)
		}

	case sdk.ActionRequeue:
		metrics.ReconcileDuration.WithLabelValues(queue, "requeue").Observe(durSec)
		delay, err := time.ParseDuration(resp.RequeueAfter)
		if err != nil {
			delay = 30 * time.Second
		}
		if err := d.completion.HandleRequeueAfter(ctx, queue, key, delay); err != nil {
			slog.Error("handle requeue failed", "queue", queue, "key", key, "error", err)
		}

	case sdk.ActionFanOut:
		metrics.ReconcileDuration.WithLabelValues(queue, "fan_out").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		// Enqueue fan-out keys first, then complete the current item.
		for _, fanKey := range resp.FanOutKeys {
			if err := d.wq.Enqueue(ctx, queue, fanKey, item.Priority); err != nil {
				slog.Error("fan-out enqueue failed", "queue", queue, "key", fanKey, "error", err)
			}
		}
		if err := d.completion.HandleSuccess(ctx, queue, key); err != nil {
			slog.Error("handle fan-out complete failed", "queue", queue, "key", key, "error", err)
		}

	default:
		slog.Error("unknown reconciler action", "queue", queue, "key", key, "action", resp.Action)
		if err := d.completion.HandleFailure(ctx, queue, key, item.Attempts, "unknown action: "+resp.Action); err != nil {
			slog.Error("handle unknown action failed", "queue", queue, "key", key, "error", err)
		}
	}
}

// sweepTick repairs counters, updates metric gauges, and handles delayed items.
func (d *Dispatcher) sweepTick(ctx context.Context) {
	// Repair the in-progress counter.
	if err := d.wq.RepairCounter(ctx, d.cfg.QueueName); err != nil {
		slog.Error("repair counter failed", "queue", d.cfg.QueueName, "error", err)
	}

	// Update gauge metrics.
	counts, err := d.wq.CountByStatus(ctx, d.cfg.QueueName)
	if err != nil {
		slog.Error("count by status failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	for status, count := range counts {
		metrics.QueueDepth.WithLabelValues(d.cfg.QueueName, string(status)).Set(float64(count))
	}

	inProg := counts[workqueue.StatusClaimed] + counts[workqueue.StatusRunning]
	metrics.InProgress.WithLabelValues(d.cfg.QueueName).Set(float64(inProg))
}

// reaperTick finds items with expired leases and requeues them.
func (d *Dispatcher) reaperTick(ctx context.Context) {
	// List items that are claimed or running with expired leases.
	claimed := workqueue.StatusClaimed
	items, err := d.wq.List(ctx, workqueue.ListFilter{
		Queue:  d.cfg.QueueName,
		Status: &claimed,
		Limit:  100,
	})
	if err != nil {
		slog.Error("reaper list failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	running := workqueue.StatusRunning
	runningItems, err := d.wq.List(ctx, workqueue.ListFilter{
		Queue:  d.cfg.QueueName,
		Status: &running,
		Limit:  100,
	})
	if err != nil {
		slog.Error("reaper list running failed", "queue", d.cfg.QueueName, "error", err)
		return
	}
	items = append(items, runningItems...)

	now := time.Now()
	reaped := 0
	for _, item := range items {
		if item.LeaseExpires != nil && item.LeaseExpires.Before(now) {
			slog.Warn("reaping expired item",
				"queue", item.Queue, "key", item.Key,
				"worker_id", item.WorkerID, "lease_expired", item.LeaseExpires,
			)
			if err := d.wq.Requeue(ctx, item.Queue, item.Key); err != nil {
				slog.Error("reaper requeue failed", "queue", item.Queue, "key", item.Key, "error", err)
				continue
			}
			reaped++
		}
	}

	if reaped > 0 {
		metrics.ItemsReaped.WithLabelValues(d.cfg.QueueName).Add(float64(reaped))
		slog.Info("reaper reclaimed items", "queue", d.cfg.QueueName, "count", reaped)
	}
}

// scaleTick adjusts worker count based on queue depth.
func (d *Dispatcher) scaleTick(ctx context.Context) {
	counts, err := d.wq.CountByStatus(ctx, d.cfg.QueueName)
	if err != nil {
		slog.Error("scale count failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	pending := counts[workqueue.StatusPending]
	inProgress := counts[workqueue.StatusClaimed] + counts[workqueue.StatusRunning]

	// Simple scaling: desired workers = min(pending + in_progress, max_concurrency).
	desired := int(pending + inProgress)
	if desired > d.cfg.MaxConcurrency {
		desired = d.cfg.MaxConcurrency
	}
	if desired < 1 && pending > 0 {
		desired = 1
	}

	if err := d.compute.EnsureWorkers(ctx, d.cfg.QueueName, desired); err != nil {
		slog.Error("scale workers failed", "queue", d.cfg.QueueName, "desired", desired, "error", err)
	}
}
