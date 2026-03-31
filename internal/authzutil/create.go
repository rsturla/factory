// Package authzutil provides shared helpers for creating authz.Authorizer
// instances from environment variables. Used by cmd/ binaries.
package authzutil

import (
	"fmt"
	"os"

	"github.com/hummingbird-org/factory-workqueue/internal/authz"
	cedarauthz "github.com/hummingbird-org/factory-workqueue/internal/authz/cedar"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/noop"
	"github.com/hummingbird-org/factory-workqueue/internal/authz/opa"
)

// CreateFromEnv creates an authz.Authorizer based on AUTHZ_BACKEND env var.
//
// Supported backends:
//   - "noop" (default): allow everything
//   - "cedar": Cedar policies from AUTHZ_CEDAR_POLICY_PATH (evaluated in-process)
//   - "opa": Open Policy Agent at AUTHZ_OPA_ENDPOINT
func CreateFromEnv() (authz.Authorizer, error) {
	backend := os.Getenv("AUTHZ_BACKEND")
	if backend == "" {
		backend = "noop"
	}

	switch backend {
	case "noop":
		return noop.Authorizer{}, nil

	case "opa":
		endpoint := os.Getenv("AUTHZ_OPA_ENDPOINT")
		if endpoint == "" {
			return nil, fmt.Errorf("opa backend requires AUTHZ_OPA_ENDPOINT")
		}
		return opa.New(opa.Config{
			Endpoint:   endpoint,
			PolicyPath: os.Getenv("AUTHZ_OPA_POLICY_PATH"),
		}), nil

	case "cedar":
		path := os.Getenv("AUTHZ_CEDAR_POLICY_PATH")
		if path == "" {
			// Fall back to AUTHZ_CEDAR_POLICY_PATH for backward compatibility.
			path = os.Getenv("AUTHZ_CEDAR_POLICY_PATH")
		}
		if path == "" {
			return nil, fmt.Errorf("cedar backend requires AUTHZ_CEDAR_POLICY_PATH (file or directory)")
		}
		return cedarauthz.NewFromPath(path)

	default:
		return nil, fmt.Errorf("unsupported authz backend: %q", backend)
	}
}
