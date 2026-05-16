// Package noop implements authz.Authorizer that allows everything.
// Use for development, testing, and deployments where auth is handled
// externally (NetworkPolicy, service mesh).
package noop

import (
	"context"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

// Authorizer allows all requests.
type Authorizer struct{}

func (Authorizer) Authorize(_ context.Context, _ authz.Request) authz.Decision {
	return authz.Decision{Allowed: true}
}

var _ authz.Authorizer = Authorizer{}
