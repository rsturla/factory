// Package metrics defines Prometheus metrics for the factory work queue platform.
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	// Counters

	ItemsEnqueued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "factory",
		Name:      "items_enqueued_total",
		Help:      "Total number of items enqueued.",
	}, []string{"queue"})

	ItemsDispatched = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "factory",
		Name:      "items_dispatched_total",
		Help:      "Total number of items dispatched to reconcilers.",
	}, []string{"queue"})

	ItemsCompleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "factory",
		Name:      "items_completed_total",
		Help:      "Total number of items completed.",
	}, []string{"queue", "outcome"})

	ItemsReaped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "factory",
		Name:      "items_reaped_total",
		Help:      "Total number of items reclaimed by the reaper (expired leases).",
	}, []string{"queue"})

	ItemsDeduped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "factory",
		Name:      "items_deduped_total",
		Help:      "Total number of enqueue requests that hit an existing pending key.",
	}, []string{"queue"})

	// Gauges

	QueueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "factory",
		Name:      "queue_depth",
		Help:      "Current number of items in a queue by status.",
	}, []string{"queue", "status"})

	WorkerCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "factory",
		Name:      "worker_count",
		Help:      "Number of registered workers.",
	}, []string{"queue", "compute_backend", "status"})

	InProgress = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "factory",
		Name:      "in_progress",
		Help:      "Number of items currently being processed.",
	}, []string{"queue"})

	MaxConcurrency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "factory",
		Name:      "max_concurrency",
		Help:      "Maximum concurrent items allowed for a queue.",
	}, []string{"queue"})

	OldestPendingAge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "factory",
		Name:      "oldest_pending_age_seconds",
		Help:      "Age in seconds of the oldest pending item. 0 if queue is empty.",
	}, []string{"queue"})

	// Histograms

	ClaimDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "factory",
		Name:      "claim_duration_seconds",
		Help:      "Time taken to claim a batch of items.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"queue"})

	ReconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "factory",
		Name:      "reconcile_duration_seconds",
		Help:      "Time taken by the reconciler to process an item.",
		Buckets:   []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"queue", "outcome"})

	WaitLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "factory",
		Name:      "wait_latency_seconds",
		Help:      "Time an item spent in pending before being claimed.",
		Buckets:   []float64{.1, .5, 1, 5, 10, 30, 60, 300, 600, 1800, 3600},
	}, []string{"queue"})

	E2ELatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "factory",
		Name:      "e2e_latency_seconds",
		Help:      "Total time from enqueue to completion.",
		Buckets:   []float64{1, 5, 10, 30, 60, 300, 600, 1800, 3600, 7200},
	}, []string{"queue"})

	AttemptsAtCompletion = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "factory",
		Name:      "attempts_at_completion",
		Help:      "Number of attempts when an item completes.",
		Buckets:   []float64{1, 2, 3, 4, 5, 10, 20},
	}, []string{"queue"})
)

// Register registers all factory metrics with the given registry.
func Register(reg prometheus.Registerer) {
	reg.MustRegister(
		ItemsEnqueued,
		ItemsDispatched,
		ItemsCompleted,
		ItemsReaped,
		ItemsDeduped,
		QueueDepth,
		WorkerCount,
		InProgress,
		MaxConcurrency,
		OldestPendingAge,
		ClaimDuration,
		ReconcileDuration,
		WaitLatency,
		E2ELatency,
		AttemptsAtCompletion,
	)
}

// RegisterDefaults registers all metrics with the default Prometheus registry.
func RegisterDefaults() {
	Register(prometheus.DefaultRegisterer)
}
