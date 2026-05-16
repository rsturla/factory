package verification

import (
	"context"
	"testing"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

func TestNoSecretsGate_Check(t *testing.T) {
	gate := &NoSecretsGate{}
	ctx := context.Background()

	tests := []struct {
		name      string
		output    map[string]any
		expectErr bool
	}{
		{
			name:      "no output",
			output:    nil,
			expectErr: false,
		},
		{
			name:      "clean output",
			output:    map[string]any{"content": "This is a clean report"},
			expectErr: false,
		},
		{
			name: "AWS key detected",
			output: map[string]any{
				"content": "Found key: AKIAIOSFODNN7EXAMPLE",
			},
			expectErr: true,
		},
		{
			name: "GitHub token detected",
			output: map[string]any{
				"token": "ghp_1234567890abcdefghijklmnopqrstuvwxyz1234",
			},
			expectErr: true,
		},
		{
			name: "GitLab token detected",
			output: map[string]any{
				"auth": "glpat-abcdefghij1234567890",
			},
			expectErr: true,
		},
		{
			name: "private key detected",
			output: map[string]any{
				"key": "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...",
			},
			expectErr: true,
		},
		{
			name: "API key pattern detected",
			output: map[string]any{
				"config": "api_key: sk_live_1234567890abcdefghijklmnop",
			},
			expectErr: true,
		},
		{
			name: "password pattern detected",
			output: map[string]any{
				"creds": "password: MyS3cr3tP@ssw0rd",
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stage := &v1.StageRun{
				Output: tt.output,
			}

			err := gate.Check(ctx, stage)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for %v", tt.output)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCalculateEntropy(t *testing.T) {
	tests := []struct {
		input    string
		minValue float64
		maxValue float64
	}{
		{"aaaaaaaaaa", 0, 0.1},            // low entropy
		{"abcdefghij", 3.0, 3.5},          // medium entropy
		{"Kj8vN2pQ9xL4mR7t", 3.5, 5.0},    // high entropy (likely random)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			entropy := calculateEntropy(tt.input)
			if entropy < tt.minValue || entropy > tt.maxValue {
				t.Errorf("entropy %f not in range [%f, %f] for %s", entropy, tt.minValue, tt.maxValue, tt.input)
			}
		})
	}
}
