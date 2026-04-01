// Package noop implements authn.Authenticator by trusting upstream proxy headers.
//
// Use this when authentication is handled by an upstream proxy (OAuth Proxy,
// Envoy, service mesh) that sets X-Forwarded-User and X-Forwarded-Groups.
package noop

import (
	"net/http"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

// Authenticator trusts identity headers set by an upstream proxy.
type Authenticator struct{}

func (Authenticator) Identify(r *http.Request) (authz.Identity, error) {
	return authz.IdentityFromRequest(r), nil
}
