package gitproxy

import (
	"testing"
)

func TestPolicy_CheckBranch(t *testing.T) {
	policy := DefaultPolicy()

	tests := []struct {
		name      string
		branch    string
		expectErr bool
	}{
		{"valid factory branch", "factory/my-fix", false},
		{"valid nested factory branch", "factory/feature/my-fix", false},
		{"invalid no prefix", "my-fix", true},
		{"invalid main", "main", true},
		{"invalid master", "master", true},
		{"invalid release", "release", true},
		{"invalid production", "production", true},
		{"invalid factory/main", "factory/main", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.CheckBranch(tt.branch)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for branch %s", tt.branch)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for branch %s: %v", tt.branch, err)
			}
		})
	}
}

func TestPolicy_CheckPaths(t *testing.T) {
	policy := DefaultPolicy()

	tests := []struct {
		name      string
		paths     []string
		expectErr bool
	}{
		{"valid paths", []string{"src/main.go", "pkg/api/types.go"}, false},
		{"env file blocked", []string{"src/main.go", ".env"}, true},
		{"credentials blocked", []string{"credentials.json"}, true},
		{"git config blocked", []string{".git/config"}, true},
		{"ssh key blocked", []string{".ssh/id_rsa"}, true},
		{"secrets dir blocked", []string{"config/secrets/api-key.txt"}, true},
		{"production env blocked", []string{"production.env"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.CheckPaths(tt.paths)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for paths %v", tt.paths)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for paths %v: %v", tt.paths, err)
			}
		})
	}
}

func TestPolicy_CheckDiffSize(t *testing.T) {
	policy := DefaultPolicy()

	tests := []struct {
		name      string
		lines     int
		files     int
		expectErr bool
	}{
		{"small diff", 100, 5, false},
		{"medium diff", 3000, 50, false},
		{"too many lines", 6000, 10, true},
		{"too many files", 100, 150, true},
		{"both exceeded", 6000, 150, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := policy.CheckDiffSize(tt.lines, tt.files)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for %d lines, %d files", tt.lines, tt.files)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for %d lines, %d files: %v", tt.lines, tt.files, err)
			}
		})
	}
}

func TestCheckAccess(t *testing.T) {
	readOnly := Access{Type: "git", Level: "read-only", URL: "github.com/org/repo"}
	readWrite := Access{Type: "git", Level: "read-write", URL: "github.com/org/repo"}

	tests := []struct {
		name      string
		access    Access
		operation string
		expectErr bool
	}{
		{"read-only fetch", readOnly, "fetch", false},
		{"read-only clone", readOnly, "clone", false},
		{"read-only push denied", readOnly, "push", true},
		{"read-write fetch", readWrite, "fetch", false},
		{"read-write push", readWrite, "push", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckAccess(tt.access, tt.operation)
			if tt.expectErr && err == nil {
				t.Errorf("expected error for %s on %s", tt.operation, tt.access.Level)
			}
			if !tt.expectErr && err != nil {
				t.Errorf("unexpected error for %s on %s: %v", tt.operation, tt.access.Level, err)
			}
		})
	}
}
