package gitproxy

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Policy defines what operations are allowed through git-proxy.
type Policy struct {
	// BranchPrefix restricts pushes to branches starting with this prefix
	BranchPrefix string

	// DenyPaths blocks commits touching any of these paths (glob patterns)
	DenyPaths []string

	// MaxDiffLines rejects commits exceeding this line count
	MaxDiffLines int

	// MaxDiffFiles rejects commits exceeding this file count
	MaxDiffFiles int
}

// DefaultPolicy returns sensible defaults for agent access.
func DefaultPolicy() Policy {
	return Policy{
		BranchPrefix: "factory/",
		DenyPaths: []string{
			"*.env",
			"credentials.*",
			".git/config",
			".ssh/*",
			"**/secrets/*",
		},
		MaxDiffLines: 5000,
		MaxDiffFiles: 100,
	}
}

// CheckBranch validates branch name against policy.
func (p *Policy) CheckBranch(branch string) error {
	if p.BranchPrefix == "" {
		return nil // no restriction
	}

	if !strings.HasPrefix(branch, p.BranchPrefix) {
		return fmt.Errorf("branch must start with %s, got %s", p.BranchPrefix, branch)
	}

	// Block protected branches even with prefix
	protected := []string{"main", "master", "release", "production"}
	for _, prot := range protected {
		if branch == prot || strings.HasSuffix(branch, "/"+prot) {
			return fmt.Errorf("cannot push to protected branch: %s", branch)
		}
	}

	return nil
}

// CheckPaths validates commit paths against deny list.
func (p *Policy) CheckPaths(paths []string) error {
	for _, path := range paths {
		for _, pattern := range p.DenyPaths {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err != nil {
				return fmt.Errorf("invalid pattern %s: %w", pattern, err)
			}
			if matched {
				return fmt.Errorf("path denied by policy: %s (matches %s)", path, pattern)
			}

			// Check full path for directory patterns
			if strings.Contains(pattern, "/") {
				matched, err = filepath.Match(pattern, path)
				if err != nil {
					return fmt.Errorf("invalid pattern %s: %w", pattern, err)
				}
				if matched {
					return fmt.Errorf("path denied by policy: %s (matches %s)", path, pattern)
				}
			}
		}
	}
	return nil
}

// CheckDiffSize validates commit size against policy.
func (p *Policy) CheckDiffSize(lineCount, fileCount int) error {
	if p.MaxDiffLines > 0 && lineCount > p.MaxDiffLines {
		return fmt.Errorf("diff too large: %d lines exceeds limit of %d", lineCount, p.MaxDiffLines)
	}
	if p.MaxDiffFiles > 0 && fileCount > p.MaxDiffFiles {
		return fmt.Errorf("diff too large: %d files exceeds limit of %d", fileCount, p.MaxDiffFiles)
	}
	return nil
}

// CheckAccess validates resource access level for operation.
func CheckAccess(access Access, operation string) error {
	switch operation {
	case "fetch", "clone", "ls-remote":
		// Read operations allowed for both levels
		return nil
	case "push":
		if access.Level != "read-write" {
			return fmt.Errorf("write operation denied: resource is read-only")
		}
		return nil
	default:
		return fmt.Errorf("unknown operation: %s", operation)
	}
}
