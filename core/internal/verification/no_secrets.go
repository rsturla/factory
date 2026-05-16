package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"

	v1 "gitlab.com/redhat/hummingbird/experimental/factory/core/pkg/api/v1"
)

// NoSecretsGate scans for leaked secrets in output.
type NoSecretsGate struct{}

// Name returns gate name.
func (g *NoSecretsGate) Name() string {
	return "no_secrets"
}

// Check scans output for secret patterns.
func (g *NoSecretsGate) Check(ctx context.Context, stage *v1.StageRun) error {
	if stage.Output == nil {
		return nil
	}

	// Serialize output to string for pattern matching
	outputJSON, err := json.Marshal(stage.Output)
	if err != nil {
		return fmt.Errorf("marshal output: %w", err)
	}
	outputStr := string(outputJSON)

	// Check each secret pattern
	for _, pattern := range secretPatterns {
		if pattern.regex.MatchString(outputStr) {
			return fmt.Errorf("potential secret detected: %s", pattern.name)
		}
	}

	// Check for high-entropy strings (potential API keys)
	if hasHighEntropyString(outputStr) {
		return fmt.Errorf("high-entropy string detected (potential API key)")
	}

	return nil
}

// secretPattern defines a regex pattern for secret detection.
type secretPattern struct {
	name  string
	regex *regexp.Regexp
}

// secretPatterns contains known secret patterns.
var secretPatterns = []secretPattern{
	{
		name:  "AWS Access Key",
		regex: regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	},
	{
		name:  "GitHub Token",
		regex: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{36,255}`),
	},
	{
		name:  "GitLab Token",
		regex: regexp.MustCompile(`glpat-[A-Za-z0-9_-]{20,}`),
	},
	{
		name:  "Generic API Key",
		regex: regexp.MustCompile(`(?i)(api[_-]?key|apikey)["\s:=]+[A-Za-z0-9_-]{20,}`),
	},
	{
		name:  "Generic Secret",
		regex: regexp.MustCompile(`(?i)(secret|password|passwd|pwd)["\s:=]+[A-Za-z0-9_!@#$%^&*-]{8,}`),
	},
	{
		name:  "Private Key",
		regex: regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	},
	{
		name:  "Bearer Token",
		regex: regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_-]{20,}`),
	},
}

// hasHighEntropyString checks for strings with high Shannon entropy.
// High entropy often indicates encrypted data or API keys.
func hasHighEntropyString(s string) bool {
	// Extract potential tokens (alphanumeric strings 20+ chars)
	tokenRegex := regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
	tokens := tokenRegex.FindAllString(s, -1)

	for _, token := range tokens {
		if calculateEntropy(token) > 4.5 {
			return true
		}
	}

	return false
}

// calculateEntropy computes Shannon entropy of a string.
func calculateEntropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}

	freq := make(map[rune]int)
	for _, c := range s {
		freq[c]++
	}

	var entropy float64
	length := float64(len(s))

	for _, count := range freq {
		p := float64(count) / length
		if p > 0 {
			entropy -= p * math.Log2(p)
		}
	}

	return entropy
}
