package inmem_test

import (
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/conformance"
	"github.com/hummingbird-org/factory-workqueue/internal/store/inmem"
)

func TestInMemConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Interface {
		s := inmem.New()
		s.EnsureQueue(nil, "test", store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
		})
		return s
	})
}
