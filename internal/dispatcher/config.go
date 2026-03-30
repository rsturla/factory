package dispatcher

import "time"

// Config controls the dispatcher's behavior.
type Config struct {
	// QueueName is the queue this dispatcher manages.
	QueueName string

	// WorkerID identifies this dispatcher instance.
	WorkerID string

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
