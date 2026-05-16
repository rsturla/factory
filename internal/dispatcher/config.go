package dispatcher

import "time"

// Mode controls what the dispatcher does.
type Mode string

const (
	// ModePush is the default — the dispatcher claims items and calls
	// the reconciler via HTTP.
	ModePush Mode = "push"

	// ModeSweepOnly runs the sweep and reaper loops but does not claim
	// or dispatch items. Standalone workers claim items themselves.
	ModeSweepOnly Mode = "sweep-only"
)

// Config controls the dispatcher's behavior.
type Config struct {
	// QueueName is the queue this dispatcher manages.
	QueueName string

	// WorkerID identifies this dispatcher instance.
	WorkerID string

	// Mode controls whether the dispatcher claims and dispatches items
	// (push) or only runs sweep and reaper loops (sweep-only).
	Mode Mode

	// DispatchInterval is how often the dispatch loop runs.
	DispatchInterval time.Duration

	// SweepInterval is how often the sweep loop runs.
	SweepInterval time.Duration

	// ReaperInterval is how often the reaper loop runs.
	ReaperInterval time.Duration

	// LeaseDuration is the default lease granted to claimed items.
	LeaseDuration time.Duration

	// BatchSize is the maximum number of items to claim per dispatch cycle.
	BatchSize int

	// MaxConcurrency for the queue (also stored in queue_state).
	MaxConcurrency int

	// MaxRetry before dead-lettering.
	MaxRetry int

	// MaxProcessingDuration is the absolute ceiling on how long a single
	// item can be processed. The heartbeat keeps the lease alive within
	// this budget, but cannot exceed it. Prevents hung reconcilers from
	// holding a concurrency slot forever. Default: 24h.
	MaxProcessingDuration time.Duration
}

// DefaultConfig returns a Config with sensible production defaults.
func DefaultConfig(queueName string) Config {
	return Config{
		QueueName:        queueName,
		DispatchInterval: 2 * time.Second,
		SweepInterval:    60 * time.Second,
		ReaperInterval:   5 * time.Minute,
		LeaseDuration:         1 * time.Hour,
		MaxProcessingDuration: 24 * time.Hour,
		BatchSize:             10,
		MaxConcurrency:   10,
		MaxRetry:         5,
	}
}
