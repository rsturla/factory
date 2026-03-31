package dispatcher

import "time"

// Mode controls what the dispatcher does.
type Mode string

const (
	// ModePush is the default — the dispatcher claims items and calls
	// the reconciler via HTTP. Used for Kubernetes reconcilers.
	ModePush Mode = "push"

	// ModeScaleOnly runs the sweep, reaper, and scale loops but does
	// not claim or dispatch items. Standalone workers claim items
	// themselves. Used for EC2/bare metal workers.
	ModeScaleOnly Mode = "scale-only"
)

// Config controls the dispatcher's behavior.
type Config struct {
	// QueueName is the queue this dispatcher manages.
	QueueName string

	// WorkerID identifies this dispatcher instance.
	WorkerID string

	// Mode controls whether the dispatcher claims and dispatches items
	// (push) or only manages scaling and reaping (scale-only).
	Mode Mode

	// DispatchInterval is how often the dispatch loop runs.
	DispatchInterval time.Duration

	// SweepInterval is how often the sweep loop runs.
	SweepInterval time.Duration

	// ReaperInterval is how often the reaper loop runs.
	ReaperInterval time.Duration

	// ScaleInterval is how often the scale loop runs.
	ScaleInterval time.Duration

	// LeaseDuration is the default lease granted to claimed items.
	LeaseDuration time.Duration

	// BatchSize is the maximum number of items to claim per dispatch cycle.
	BatchSize int

	// MaxConcurrency for the queue (also stored in queue_state).
	MaxConcurrency int

	// MaxRetry before dead-lettering.
	MaxRetry int
}

// DefaultConfig returns a Config with sensible production defaults.
func DefaultConfig(queueName string) Config {
	return Config{
		QueueName:        queueName,
		DispatchInterval: 2 * time.Second,
		SweepInterval:    60 * time.Second,
		ReaperInterval:   5 * time.Minute,
		ScaleInterval:    30 * time.Second,
		LeaseDuration:    1 * time.Hour,
		BatchSize:        10,
		MaxConcurrency:   10,
		MaxRetry:         5,
	}
}
