// Package authzutil provides shared helpers for creating authz.Authorizer
// instances from environment variables. Used by cmd/ binaries.
package authzutil

import (
	"fmt"
	"os"

	"github.com/hummingbird-org/factory/internal/authz"
	cedarauthz "github.com/hummingbird-org/factory/internal/authz/cedar"
	"github.com/hummingbird-org/factory/internal/authz/headergroups"
	"github.com/hummingbird-org/factory/internal/authz/noop"
	"github.com/hummingbird-org/factory/internal/authz/opa"
)

// CreateFromEnv creates an authz.Authorizer based on AUTHZ_BACKEND env var.
//
// Supported backends:
//   - "noop" (default): allow everything
//   - "headergroups": group-based rules from AUTHZ_CONFIG_FILE or AUTHZ_CONFIG
//   - "opa": Open Policy Agent at AUTHZ_OPA_ENDPOINT
//   - "cedar": Cedar policies from AUTHZ_POLICY_FILE (evaluated in-process)
func CreateFromEnv() (authz.Authorizer, error) {
	backend := os.Getenv("AUTHZ_BACKEND")
	if backend == "" {
		backend = "noop"
	}

	switch backend {
	case "noop":
		return noop.Authorizer{}, nil

	case "headergroups":
		if path := os.Getenv("AUTHZ_CONFIG_FILE"); path != "" {
			return headergroups.NewFromFile(path)
		}
		if data := os.Getenv("AUTHZ_CONFIG"); data != "" {
			return headergroups.NewFromJSON(data)
		}
		return nil, fmt.Errorf("headergroups backend requires AUTHZ_CONFIG_FILE or AUTHZ_CONFIG")

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
		path := os.Getenv("AUTHZ_POLICY_FILE")
		if path == "" {
			return nil, fmt.Errorf("cedar backend requires AUTHZ_POLICY_FILE")
		}
		return cedarauthz.NewFromFile(path)

	default:
		return nil, fmt.Errorf("unsupported authz backend: %q", backend)
	}
}
