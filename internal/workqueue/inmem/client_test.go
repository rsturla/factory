package inmem_test

import (
	"testing"

	"github.com/hummingbird-org/factory/internal/workqueue"
	"github.com/hummingbird-org/factory/internal/workqueue/conformance"
	"github.com/hummingbird-org/factory/internal/workqueue/inmem"
)

func TestInMemConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) workqueue.Interface {
		c := inmem.New()
		if err := c.EnsureQueue(nil, "test", workqueue.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
			ComputeBackend: "kubernetes",
		}); err != nil {
			t.Fatal(err)
		}
		return c
	})
}
