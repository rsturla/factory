package completion_test

import (
	"context"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/internal/completion"
	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func FuzzBackoffHandleFailure(f *testing.F) {
	// Fuzz the attempt number to find overflow/NaN/Inf in the backoff math.
	f.Add(0)
	f.Add(1)
	f.Add(2)
	f.Add(5)
	f.Add(-1)
	f.Add(-100)
	f.Add(63) // 2^62 overflows int64 nanoseconds
	f.Add(64)
	f.Add(100)
	f.Add(1000)
	f.Add(1<<31 - 1)

	f.Fuzz(func(t *testing.T, attempt int) {
		ctx := context.Background()
		s := inmem.New()
		s.EnsureQueue(ctx, "test", store.QueueConfig{MaxConcurrency: 10, MaxRetry: 1000})
		s.Enqueue(ctx, "test", "k", 0)
		s.ClaimBatch(ctx, "test", 1, "w", time.Hour)

		maxAttempts := attempt + 1
		if maxAttempts <= 0 {
			maxAttempts = 1<<31 - 1 // overflow-safe: ensure we requeue to exercise backoff
		}
		h := completion.NewHandler(s, completion.Config{
			MaxAttempts:    maxAttempts,
			BackoffBase:    100 * time.Millisecond,
			BackoffMax:     10 * time.Minute,
			JitterFraction: 0.25,
		})

		// Must not panic or produce infinite/NaN delays.
		h.HandleFailure(ctx, "test", "k", attempt, "error")

		item, err := s.GetItem(ctx, "test", "k")
		if err != nil {
			return // item may not exist if deadlettered
		}
		if item.NotBefore != nil {
			delay := time.Until(*item.NotBefore)
			// BackoffMax is 10m, jitter adds up to 25%, so max is 12.5m.
			// Allow 13m to account for clock skew.
			if delay > 13*time.Minute {
				t.Errorf("backoff delay exceeds BackoffMax+jitter: attempt=%d, delay=%v", attempt, delay)
			}
		}
	})
}
