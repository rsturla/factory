// Package dispatcher implements the core dispatch engine for the factory work queue.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/hummingbird-org/factory-workqueue/internal/completion"
	"github.com/hummingbird-org/factory-workqueue/internal/compute"
	"github.com/hummingbird-org/factory-workqueue/internal/metrics"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/tracing"
	"github.com/hummingbird-org/factory-workqueue/pkg/client"
	"github.com/hummingbird-org/factory-workqueue/pkg/sdk"
)

// Dispatcher manages the lifecycle of work items for a single queue.
type Dispatcher struct {
	store      store.Interface
	reconciler *client.ReconcilerClient
	compute    compute.Provider
	completion *completion.Handler
	cfg        Config
	inFlight   sync.WaitGroup

	leaderMu   sync.RWMutex
	isLeader   bool
	leaderTTL  time.Duration
	sweepCount int
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

	mode := d.cfg.Mode
	if mode == "" {
		mode = ModePush
	}

	slog.Info("dispatcher starting",
		"queue", d.cfg.QueueName,
		"worker_id", d.cfg.WorkerID,
		"mode", mode,
		"max_concurrency", d.cfg.MaxConcurrency,
	)

	leaderInterval := d.cfg.LeaderInterval
	if leaderInterval <= 0 {
		leaderInterval = 5 * time.Second
	}
	leaderTTL := d.cfg.LeaderTTL
	if leaderTTL <= 0 {
		leaderTTL = 15 * time.Second
	}
	d.leaderTTL = leaderTTL

	g, gctx := errgroup.WithContext(ctx)

	// Leader loop runs without jitter — must acquire leadership immediately.
	g.Go(func() error { return d.loop(gctx, "leader", leaderInterval, d.leaderTick, false) })

	if mode == ModePush {
		g.Go(func() error { return d.loop(gctx, "dispatch", d.cfg.DispatchInterval, d.dispatchTick, true) })
	}

	// Sweep, reaper, and scale run in all modes.
	g.Go(func() error { return d.loop(gctx, "sweep", d.cfg.SweepInterval, d.sweepTick, true) })
	g.Go(func() error { return d.loop(gctx, "reaper", d.cfg.ReaperInterval, d.reaperTick, true) })
	g.Go(func() error { return d.loop(gctx, "scale", d.cfg.ScaleInterval, d.scaleTick, true) })
	err := g.Wait()

	slog.Info("draining in-flight work", "queue", d.cfg.QueueName)
	d.inFlight.Wait()
	slog.Info("dispatcher stopped", "queue", d.cfg.QueueName)
	return err
}

func (d *Dispatcher) loop(ctx context.Context, name string, interval time.Duration, tick func(context.Context), jitterEnabled bool) error {
	if jitterEnabled {
		jitter := time.Duration(rand.Int64N(int64(interval)))
		select {
		case <-time.After(jitter):
		case <-ctx.Done():
			return nil
		}
	}
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

func (d *Dispatcher) leaderTick(ctx context.Context) {
	ok, err := d.store.TryLeader(ctx, d.cfg.QueueName, d.cfg.WorkerID, d.leaderTTL)
	if err != nil {
		slog.Error("leader election failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	d.leaderMu.Lock()
	wasLeader := d.isLeader
	d.isLeader = ok
	d.leaderMu.Unlock()

	if ok && !wasLeader {
		slog.Info("acquired leadership", "queue", d.cfg.QueueName, "worker_id", d.cfg.WorkerID)
		metrics.LeaderStatus.WithLabelValues(d.cfg.QueueName).Set(1)
	} else if !ok && wasLeader {
		slog.Warn("lost leadership", "queue", d.cfg.QueueName, "worker_id", d.cfg.WorkerID)
		metrics.LeaderStatus.WithLabelValues(d.cfg.QueueName).Set(0)
	}
}

func (d *Dispatcher) dispatchTick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	d.leaderMu.RLock()
	leader := d.isLeader
	d.leaderMu.RUnlock()
	if !leader {
		return
	}

	if paused, _ := d.store.IsQueuePaused(ctx, d.cfg.QueueName); paused {
		return
	}

	start := time.Now()
	items, err := d.store.ClaimBatch(ctx, d.cfg.QueueName, d.cfg.BatchSize, d.cfg.WorkerID, d.cfg.LeaseDuration)
	if err != nil {
		slog.Error("claim batch failed", "queue", d.cfg.QueueName, "error", err)
		metrics.StoreErrors.WithLabelValues(d.cfg.QueueName, "claim").Inc()
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
		itemCtx, itemCancel := context.WithTimeout(context.Background(), d.cfg.LeaseDuration)
		go func(item store.WorkItem, cancel context.CancelFunc) {
			defer cancel()
			d.processItem(itemCtx, item)
		}(item, itemCancel)
	}
}

func (d *Dispatcher) processItem(ctx context.Context, item store.WorkItem) {
	defer d.inFlight.Done()

	tracer := tracing.Tracer("factory.dispatcher")

	// Link to the enqueue trace so operators can navigate from the
	// dispatch trace to the enqueue trace and vice versa.
	var spanOpts []trace.SpanStartOption
	if history, err := d.store.GetItemHistory(ctx, item.Queue, item.Key); err == nil {
		for _, h := range history {
			if h.TraceID != "" && h.ToStatus == "pending" {
				if remoteCtx, ok := parseTraceparent(h.TraceID); ok {
					spanOpts = append(spanOpts, trace.WithLinks(trace.Link{
						SpanContext: remoteCtx,
					}))
				}
				break
			}
		}
	}

	spanOpts = append(spanOpts, trace.WithAttributes(
		attribute.String("queue", item.Queue),
		attribute.String("key", item.Key),
		attribute.Int("priority", item.Priority),
		attribute.Int("attempt", item.Attempts),
	))

	ctx, span := tracer.Start(ctx, "processItem", spanOpts...)
	defer span.End()

	traceID := span.SpanContext().TraceID().String()

	// Transition claimed → running.
	func() {
		_, transitionSpan := tracer.Start(ctx, "transition",
			trace.WithAttributes(attribute.String("from", "claimed"), attribute.String("to", "running")))
		defer transitionSpan.End()

		if err := d.store.Transition(ctx, item.Queue, item.Key, store.StatusClaimed, store.StatusRunning,
			store.WithWorkerID(d.cfg.WorkerID)); err != nil {
			transitionSpan.RecordError(err)
			transitionSpan.SetStatus(codes.Error, "transition failed")
			span.RecordError(err)
			span.SetStatus(codes.Error, "transition failed")
			slog.Error("transition to running failed", "queue", item.Queue, "key", item.Key, "error", err, "trace_id", traceID)
			metrics.StoreErrors.WithLabelValues(d.cfg.QueueName, "transition").Inc()
		}
	}()

	if item.ClaimedAt != nil {
		waitSec := item.ClaimedAt.Sub(item.CreatedAt).Seconds()
		span.SetAttributes(attribute.Float64("wait_latency_s", waitSec))
		metrics.WaitLatency.WithLabelValues(item.Queue).Observe(waitSec)
	}

	// Record history with trace ID for correlation.
	d.store.RecordHistory(ctx, store.HistoryEntry{
		Queue: item.Queue, Key: item.Key,
		FromStatus: "claimed", ToStatus: "running",
		WorkerID: d.cfg.WorkerID, TraceID: traceID,
	})

	// Call the reconciler.
	var resp sdk.ProcessResponse
	var reconcileDur float64
	var reconcileErr error
	func() {
		_, reconcileSpan := tracer.Start(ctx, "reconcile",
			trace.WithAttributes(attribute.String("endpoint", d.cfg.WorkerID)))
		defer reconcileSpan.End()

		start := time.Now()
		resp, reconcileErr = d.reconciler.Process(ctx, sdk.ProcessRequest{
			Key:      item.Key,
			Attempt:  item.Attempts,
			Priority: item.Priority,
			TraceID:  traceID,
		})
		reconcileDur = time.Since(start).Seconds()
		reconcileSpan.SetAttributes(attribute.Float64("duration_s", reconcileDur))

		if reconcileErr != nil {
			reconcileSpan.RecordError(reconcileErr)
			reconcileSpan.SetStatus(codes.Error, "reconciler unreachable")
		} else if resp.Error != "" {
			reconcileSpan.RecordError(fmt.Errorf("%s", resp.Error))
			reconcileSpan.SetStatus(codes.Error, "reconciler error")
		}
	}()

	span.SetAttributes(attribute.Float64("reconcile_duration_s", reconcileDur))

	if reconcileErr != nil {
		span.RecordError(reconcileErr)
		span.SetStatus(codes.Error, "reconciler unreachable")
		slog.Error("reconciler call failed", "queue", item.Queue, "key", item.Key, "error", reconcileErr, "trace_id", traceID)
		metrics.ReconcileDuration.WithLabelValues(item.Queue, "infra_error").Observe(reconcileDur)

		func() {
			_, infraSpan := tracer.Start(ctx, "handleInfraFailure")
			defer infraSpan.End()
			if err := d.completion.HandleInfraFailure(ctx, item.Queue, item.Key); err != nil {
				infraSpan.RecordError(err)
				slog.Error("handle infra failure failed", "queue", item.Queue, "key", item.Key, "error", err, "trace_id", traceID)
			}
		}()
		return
	}

	d.handleResponse(ctx, item, resp, reconcileDur, span, traceID)
}

func (d *Dispatcher) handleResponse(ctx context.Context, item store.WorkItem, resp sdk.ProcessResponse, durSec float64, span trace.Span, traceID string) {
	queue, key := item.Queue, item.Key

	if resp.Error != "" {
		span.RecordError(fmt.Errorf("%s", resp.Error))
		span.SetStatus(codes.Error, "reconciler error")
		span.SetAttributes(attribute.String("outcome", "failed"))
		slog.Warn("reconciler reported error", "queue", queue, "key", key, "error", resp.Error, "trace_id", traceID)
		metrics.ReconcileDuration.WithLabelValues(queue, "failed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "failed").Inc()
		d.completion.HandleFailure(ctx, queue, key, item.Attempts, resp.Error)
		return
	}

	tracer := tracing.Tracer("factory.dispatcher")

	switch resp.Action {
	case sdk.ActionCompleted:
		span.SetAttributes(attribute.String("outcome", "completed"))
		metrics.ReconcileDuration.WithLabelValues(queue, "completed").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		func() {
			_, s := tracer.Start(ctx, "complete")
			defer s.End()
			d.completion.HandleSuccess(ctx, queue, key)
		}()

	case sdk.ActionConverged:
		span.SetAttributes(attribute.String("outcome", "converged"))
		metrics.ReconcileDuration.WithLabelValues(queue, "converged").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "converged").Inc()
		metrics.AttemptsAtCompletion.WithLabelValues(queue).Observe(float64(item.Attempts))
		metrics.E2ELatency.WithLabelValues(queue).Observe(time.Since(item.CreatedAt).Seconds())
		func() {
			_, s := tracer.Start(ctx, "complete")
			defer s.End()
			d.completion.HandleSuccess(ctx, queue, key)
		}()

	case sdk.ActionRequeue:
		span.SetAttributes(attribute.String("outcome", "requeue"))
		metrics.ReconcileDuration.WithLabelValues(queue, "requeue").Observe(durSec)
		delay, err := time.ParseDuration(resp.RequeueAfter)
		if err != nil {
			delay = 30 * time.Second
		}
		func() {
			_, s := tracer.Start(ctx, "requeueAfter", trace.WithAttributes(attribute.String("delay", delay.String())))
			defer s.End()
			d.completion.HandleRequeueAfter(ctx, queue, key, delay)
		}()

	case sdk.ActionFanOut:
		span.SetAttributes(attribute.String("outcome", "fan_out"), attribute.Int("fan_out_count", len(resp.FanOutKeys)))
		metrics.ReconcileDuration.WithLabelValues(queue, "fan_out").Observe(durSec)
		metrics.ItemsCompleted.WithLabelValues(queue, "succeeded").Inc()
		func() {
			_, s := tracer.Start(ctx, "fanOut", trace.WithAttributes(attribute.Int("count", len(resp.FanOutKeys))))
			defer s.End()
			batch := make([]store.BatchEnqueueItem, len(resp.FanOutKeys))
			for i, fanKey := range resp.FanOutKeys {
				batch[i] = store.BatchEnqueueItem{Key: fanKey, Priority: item.Priority}
			}
			if _, err := d.store.EnqueueBatch(ctx, queue, batch); err != nil {
				slog.Error("fan-out enqueue failed", "queue", queue, "error", err, "trace_id", traceID)
				metrics.StoreErrors.WithLabelValues(queue, "enqueue_batch").Inc()
			}
			d.completion.HandleSuccess(ctx, queue, key)
		}()

	default:
		span.SetStatus(codes.Error, "unknown action")
		slog.Error("unknown reconciler action", "queue", queue, "key", key, "action", resp.Action, "trace_id", traceID)
		d.completion.HandleFailure(ctx, queue, key, item.Attempts, "unknown action: "+resp.Action)
	}
}

func (d *Dispatcher) sweepTick(ctx context.Context) {
	d.sweepCount++

	// Count active statuses every tick (cheap — only scans pending/claimed/running).
	// Count all statuses every 10th tick (~10 minutes) for dashboard metrics.
	var statuses []store.Status
	fullSweep := d.sweepCount%10 == 0
	if !fullSweep {
		statuses = []store.Status{store.StatusPending, store.StatusClaimed, store.StatusRunning}
	}

	counts, err := d.store.CountByStatus(ctx, d.cfg.QueueName, statuses...)
	if err != nil {
		slog.Error("count by status failed", "queue", d.cfg.QueueName, "error", err)
		return
	}

	allStatuses := []store.Status{
		store.StatusPending, store.StatusClaimed, store.StatusRunning,
		store.StatusSucceeded, store.StatusFailed, store.StatusDeadLetter,
	}
	for _, status := range allStatuses {
		if c, ok := counts[status]; ok {
			metrics.QueueDepth.WithLabelValues(d.cfg.QueueName, string(status)).Set(float64(c))
		} else if fullSweep {
			metrics.QueueDepth.WithLabelValues(d.cfg.QueueName, string(status)).Set(0)
		}
	}
	inProg := counts[store.StatusClaimed] + counts[store.StatusRunning]
	metrics.InProgress.WithLabelValues(d.cfg.QueueName).Set(float64(inProg))
	metrics.MaxConcurrency.WithLabelValues(d.cfg.QueueName).Set(float64(d.cfg.MaxConcurrency))

	// Oldest pending item age — hits the partial index, very cheap.
	pending := store.StatusPending
	oldest, err := d.store.List(ctx, store.ListFilter{Queue: d.cfg.QueueName, Status: &pending, Limit: 1})
	if err == nil && len(oldest) > 0 {
		metrics.OldestPendingAge.WithLabelValues(d.cfg.QueueName).Set(time.Since(oldest[0].CreatedAt).Seconds())
	} else {
		metrics.OldestPendingAge.WithLabelValues(d.cfg.QueueName).Set(0)
	}

	// Repair the in_progress counter if it drifted (e.g., after a crash).
	// Only the leader repairs, and only on full-sweep ticks (~every 10
	// minutes) since drift is rare and the repair query is not free.
	d.leaderMu.RLock()
	leader := d.isLeader
	d.leaderMu.RUnlock()
	if leader && fullSweep {
		d.store.RepairCounter(ctx, d.cfg.QueueName)
	}
}

func (d *Dispatcher) reaperTick(ctx context.Context) {
	d.leaderMu.RLock()
	leader := d.isLeader
	d.leaderMu.RUnlock()
	if !leader {
		return
	}

	reaped := 0
	for {
		items, err := d.store.ListExpiredLeases(ctx, d.cfg.QueueName, 100)
		if err != nil {
			slog.Error("list expired leases failed", "queue", d.cfg.QueueName, "error", err)
			break
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			slog.Warn("reaping expired item", "queue", item.Queue, "key", item.Key)
			if err := d.store.Requeue(ctx, item.Queue, item.Key); err == nil {
				reaped++
			}
		}
		if len(items) < 100 {
			break
		}
	}

	if reaped > 0 {
		metrics.ItemsReaped.WithLabelValues(d.cfg.QueueName).Add(float64(reaped))
	}
}

func (d *Dispatcher) scaleTick(ctx context.Context) {
	d.leaderMu.RLock()
	leader := d.isLeader
	d.leaderMu.RUnlock()
	if !leader {
		return
	}

	counts, err := d.store.CountByStatus(ctx, d.cfg.QueueName, store.StatusPending, store.StatusClaimed, store.StatusRunning)
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

// parseTraceparent parses a W3C traceparent string ("00-{traceID}-{spanID}-{flags}")
// into a SpanContext for creating span links across the async queue boundary.
func parseTraceparent(tp string) (trace.SpanContext, bool) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return trace.SpanContext{}, false
	}

	traceID, err := trace.TraceIDFromHex(parts[1])
	if err != nil {
		return trace.SpanContext{}, false
	}

	spanID, err := trace.SpanIDFromHex(parts[2])
	if err != nil {
		return trace.SpanContext{}, false
	}

	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}), true
}
