// Package dispatcher implements the core dispatch engine for the factory work queue.
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
	"github.com/hummingbird-org/factory/internal/store"
	"github.com/hummingbird-org/factory/pkg/client"
	"github.com/hummingbird-org/factory/pkg/sdk"
)

// Dispatcher manages the lifecycle of work items for a single queue.
type Dispatcher struct {
	store      store.Interface
	reconciler *client.ReconcilerClient
	compute    compute.Provider
	completion *completion.Handler
	cfg        Config
	inFlight   sync.WaitGroup
}

// New creates a new Dispatcher.
func New(s store.Interface, reconciler *client.ReconcilerClient, cp compute.Provider, cfg Config) *Dispatcher {
	compCfg := completion.Config{
		MaxAttempts:    cfg.MaxRetry,
		BackoffBase:    30 * time.Second,
		BackoffMax:     10 * time.Minute,
		JitterFraction: 0.25,
	}
	return &Dispatcher{
		store:      s,
		reconciler: reconciler,
		compute:    cp,
		completion: completion.NewHandler(s, compCfg),
		cfg:        cfg,
	}
}

// Run starts the dispatcher loops and blocks until the context is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.store.EnsureQueue(ctx, d.cfg.QueueName, store.QueueConfig{
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
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return d.loop(gctx, "dispatch", d.cfg.DispatchInterval, d.dispatchTick) })
	g.Go(func() error { return d.loop(gctx, "sweep", d.cfg.SweepInterval, d.sweepTick) })
	g.Go(func() error { return d.loop(gctx, "reaper", d.cfg.ReaperInterval, d.reaperTick) })
	g.Go(func() error { return d.loop(gctx, "scale", d.cfg.ScaleInterval, d.scaleTick) })
	err := g.Wait()

	slog.Info("draining in-flight work", "queue", d.cfg.QueueName)
	d.inFlight.Wait()
	slog.Info("dispatcher stopped", "queue", d.cfg.QueueName)
	return err
}

func (d *Dispatcher) loop(ctx context.Context, name string, interval time.Duration, tick func(context.Context)) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
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

func (d *Dispatcher) dispatchTick(ctx context.Context) {
	start := time.Now()
	items, err := d.store.ClaimBatch(ctx, d.cfg.QueueName, d.cfg.BatchSize, d.cfg.WorkerID, d.cfg.LeaseDuration)
	if err != nil {
		slog.Error("claim batch failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	metrics.ClaimDuration.WithLabelValues(d.cfg.QueueName).Observe(time.Since(start).Seconds())

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

func (d *Dispatcher) processItem(ctx context.Context, item store.WorkItem) {
	defer d.inFlight.Done()

	if err := d.store.Transition(ctx, item.Queue, item.Key, store.StatusClaimed, store.StatusRunning,
		store.WithWorkerID(d.cfg.WorkerID)); err != nil {
		slog.Error("transition to running failed", "queue", item.Queue, "key", item.Key, "error", err)
		return
	}

	if item.ClaimedAt != nil {
		metrics.WaitLatency.WithLabelValues(item.Queue).Observe(item.ClaimedAt.Sub(item.CreatedAt).Seconds())
	}

	start := time.Now()
	resp, err := d.reconciler.Process(ctx, sdk.ProcessRequest{
		Key:      item.Key,
		Attempt:  item.Attempts,
		Priority: item.Priority,
	})
	reconcileDur := time.Since(start).Seconds()

	if err != nil {
		slog.Error("reconciler call failed", "queue", item.Queue, "key", item.Key, "error", err)
		metrics.ReconcileDuration.WithLabelValues(item.Queue, "infra_error").Observe(reconcileDur)
		if infraErr := d.completion.HandleInfraFailure(ctx, item.Queue, item.Key); infraErr != nil {
			slog.Error("handle infra failure failed", "queue", item.Queue, "key", item.Key, "error", infraErr)
		}
		return
	}

	d.handleResponse(ctx, item, resp, reconcileDur)
}

func (d *Dispatcher) handleResponse(ctx context.Context, item store.WorkItem, resp sdk.ProcessResponse, durSec float64) {
	queue, key := item.Queue, item.Key

	if resp.Error != "" {
		slog.Warn("reconciler reported error", "queue", queue, "key", key, "error", resp.Error)
		metrics.ReconcileDuration.WithLabelValues(queue, "failed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "failed").Inc()
		d.completion.HandleFailure(ctx, queue, key, item.Attempts, resp.Error)
		return
	}

	switch resp.Action {
	case sdk.ActionCompleted:
		metrics.ReconcileDuration.WithLabelValues(queue, "completed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		d.completion.HandleSuccess(ctx, queue, key)

	case sdk.ActionConverged:
		metrics.ReconcileDuration.WithLabelValues(queue, "converged").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "converged").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		d.completion.HandleSuccess(ctx, queue, key)

	case sdk.ActionRequeue:
		metrics.ReconcileDuration.WithLabelValues(queue, "requeue").Observe(durSec)
		delay, err := time.ParseDuration(resp.RequeueAfter)
		if err != nil {
			delay = 30 * time.Second
		}
		d.completion.HandleRequeueAfter(ctx, queue, key, delay)

	case sdk.ActionFanOut:
		metrics.ReconcileDuration.WithLabelValues(queue, "fan_out").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		for _, fanKey := range resp.FanOutKeys {
			if err := d.store.Enqueue(ctx, queue, fanKey, item.Priority); err != nil {
				slog.Error("fan-out enqueue failed", "queue", queue, "key", fanKey, "error", err)
			}
		}
		d.completion.HandleSuccess(ctx, queue, key)

	default:
		slog.Error("unknown reconciler action", "queue", queue, "key", key, "action", resp.Action)
		d.completion.HandleFailure(ctx, queue, key, item.Attempts, "unknown action: "+resp.Action)
	}
}

func (d *Dispatcher) sweepTick(ctx context.Context) {
	d.store.RepairCounter(ctx, d.cfg.QueueName)

	counts, err := d.store.CountByStatus(ctx, d.cfg.QueueName)
	if err != nil {
		slog.Error("count by status failed", "queue", d.cfg.QueueName, "error", err)
		return
	}
	for status, count := range counts {
		metrics.QueueDepth.WithLabelValues(d.cfg.QueueName, string(status)).Set(float64(count))
	}
	inProg := counts[store.StatusClaimed] + counts[store.StatusRunning]
	metrics.InProgress.WithLabelValues(d.cfg.QueueName).Set(float64(inProg))
}

func (d *Dispatcher) reaperTick(ctx context.Context) {
	claimed := store.StatusClaimed
	running := store.StatusRunning
	items, _ := d.store.List(ctx, store.ListFilter{Queue: d.cfg.QueueName, Status: &claimed, Limit: 100})
	runningItems, _ := d.store.List(ctx, store.ListFilter{Queue: d.cfg.QueueName, Status: &running, Limit: 100})
	items = append(items, runningItems...)

	now := time.Now()
	reaped := 0
	for _, item := range items {
		if item.LeaseExpires != nil && item.LeaseExpires.Before(now) {
			slog.Warn("reaping expired item", "queue", item.Queue, "key", item.Key)
			if err := d.store.Requeue(ctx, item.Queue, item.Key); err == nil {
				reaped++
			}
		}
	}
	if reaped > 0 {
		metrics.ItemsReaped.WithLabelValues(d.cfg.QueueName).Add(float64(reaped))
	}
}

func (d *Dispatcher) scaleTick(ctx context.Context) {
	counts, err := d.store.CountByStatus(ctx, d.cfg.QueueName)
	if err != nil {
		return
	}
	pending := counts[store.StatusPending]
	inProgress := counts[store.StatusClaimed] + counts[store.StatusRunning]
	desired := int(pending + inProgress)
	if desired > d.cfg.MaxConcurrency {
		desired = d.cfg.MaxConcurrency
	}
	if desired < 1 && pending > 0 {
		desired = 1
	}
	d.compute.EnsureWorkers(ctx, d.cfg.QueueName, desired)
}
