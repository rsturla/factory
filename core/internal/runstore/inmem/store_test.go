package inmem

import (
	"testing"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/conformance"
)

func TestInmemStore(t *testing.T) {
	conformance.TestSuite(t, func(t *testing.T) runstore.Store {
		return New()
	})
}
