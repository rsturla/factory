package sandbox

import (
	"fmt"
	"os"
)

// ProviderType identifies sandbox provider implementation.
type ProviderType string

const (
	ProviderTypeMock      ProviderType = "mock"
	ProviderTypeDocker    ProviderType = "docker"
	ProviderTypeOpenShell ProviderType = "openshell"
)

// ProviderConfig configures sandbox provider creation.
type ProviderConfig struct {
	Type ProviderType

	// OpenShell-specific
	OpenShellEndpoint string
}

// NewProvider creates appropriate sandbox provider based on config.
func NewProvider(cfg ProviderConfig) (SandboxProvider, error) {
	switch cfg.Type {
	case ProviderTypeMock:
		return NewMockProvider(), nil

	case ProviderTypeDocker:
		return NewDockerProvider()

	case ProviderTypeOpenShell:
		if cfg.OpenShellEndpoint == "" {
			return nil, fmt.Errorf("openshell endpoint required")
		}
		return NewOpenShellProvider(cfg.OpenShellEndpoint)

	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}

// NewProviderFromEnv creates provider from environment variables.
// SANDBOX_PROVIDER: mock|docker|openshell (default: docker)
// OPENSHELL_ENDPOINT: gRPC endpoint for OpenShell (required if provider=openshell)
func NewProviderFromEnv() (SandboxProvider, error) {
	providerType := os.Getenv("SANDBOX_PROVIDER")
	if providerType == "" {
		providerType = "docker"
	}

	cfg := ProviderConfig{
		Type:              ProviderType(providerType),
		OpenShellEndpoint: os.Getenv("OPENSHELL_ENDPOINT"),
	}

	return NewProvider(cfg)
}
