package sqlite_test

import (
	"context"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	"github.com/hummingbird-org/factory-workqueue/internal/store/conformance"
	"github.com/hummingbird-org/factory-workqueue/internal/store/sqlite"
)

func TestSQLiteConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Interface {
		s, err := sqlite.New(":memory:")
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		if err := s.EnsureQueue(context.Background(), "test", store.QueueConfig{
			MaxConcurrency: 10,
			MaxRetry:       5,
		}); err != nil {
			t.Fatalf("EnsureQueue: %v", err)
		}
		return s
	})
}
