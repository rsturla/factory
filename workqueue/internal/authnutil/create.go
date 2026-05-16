// Package authnutil provides shared helpers for creating authn.Authenticator
// instances from environment variables. Used by cmd/ binaries.
package authnutil

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/hummingbird-org/factory-workqueue/internal/authn"
	"github.com/hummingbird-org/factory-workqueue/internal/authn/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/authn/openshift"
)

// CreateFromEnv creates an authn.Authenticator based on AUTHN_BACKEND env var.
//
// Supported backends:
//   - "noop" (default): trust X-Forwarded-User/X-Forwarded-Groups headers
//   - "openshift": validate Bearer token via OpenShift user API
func CreateFromEnv() (authn.Authenticator, error) {
	backend := os.Getenv("AUTHN_BACKEND")
	if backend == "" {
		backend = "noop"
	}

	switch backend {
	case "noop":
		slog.Warn("using noop authentication: all X-Forwarded-User headers are trusted without verification; set AUTHN_BACKEND for production")
		return noop.Authenticator{}, nil

	case "openshift":
		return openshift.New()

	default:
		return nil, fmt.Errorf("unsupported authn backend: %q", backend)
	}
}
