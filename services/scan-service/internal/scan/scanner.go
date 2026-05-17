package scan

import "context"

// ScanMeta contains metadata about a scan run.
type ScanMeta struct {
	DBVersion string
}

// Finding represents a single vulnerability finding from a scanner.
type Finding struct {
	VulnID         string
	Severity       string
	PackageName    string
	PackageVersion string
	PackageType    string
	FixedVersion   string
}

// Scanner is the interface for vulnerability scanners.
type Scanner interface {
	Name() string
	Scan(ctx context.Context, sbomBytes []byte) ([]Finding, ScanMeta, error)
}
