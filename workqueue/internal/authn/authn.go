// Package authn defines the authentication interface for the factory platform.
//
// Authentication is pluggable — swap backends by setting AUTHN_BACKEND.
// Each backend resolves an HTTP request into an authz.Identity (user + groups).
//
// Available backends:
//   - noop: trust X-Forwarded-User/X-Forwarded-Groups headers (proxy/mesh)
//   - openshift: validate Bearer token via OpenShift user API
//
// Adding a new backend:
//  1. Implement authn.Authenticator in a new package
//  2. Register it in internal/authnutil/create.go
package authn

import (
	"net/http"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
)

// Authenticator resolves an HTTP request into a caller identity.
// Implementations should be safe for concurrent use.
type Authenticator interface {
	Identify(r *http.Request) (authz.Identity, error)
}
