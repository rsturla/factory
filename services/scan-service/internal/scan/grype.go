package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// GrypeScanner scans SBOMs for vulnerabilities using the Grype vulnerability database.
//
// TODO: integrate the Grype Go library for native in-process scanning.
// The Grype library API is complex and tightly coupled to its CLI, so this
// implementation is a stub that parses the SBOM and returns empty findings.
type GrypeScanner struct {
	dbPath string
}

// NewGrypeScanner creates a new GrypeScanner that reads its vulnerability
// database from dbPath.
func NewGrypeScanner(dbPath string) *GrypeScanner {
	return &GrypeScanner{dbPath: dbPath}
}

func (g *GrypeScanner) Name() string { return "grype" }

func (g *GrypeScanner) Scan(_ context.Context, sbomBytes []byte) ([]Finding, ScanMeta, error) {
	if len(sbomBytes) == 0 {
		return nil, ScanMeta{}, fmt.Errorf("empty SBOM data")
	}

	// Validate the SBOM is parseable JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(sbomBytes, &raw); err != nil {
		return nil, ScanMeta{}, fmt.Errorf("decode sbom: %w", err)
	}

	// Check for a recognizable SBOM format.
	_, hasSPDX := raw["spdxVersion"]
	_, hasCDX := raw["bomFormat"]
	if !hasSPDX && !hasCDX {
		return nil, ScanMeta{}, fmt.Errorf("unrecognized SBOM format: expected spdxVersion or bomFormat field")
	}

	// Extract SBOM format for logging context.
	format := "unknown"
	if hasSPDX {
		format = "spdx-json"
	} else if hasCDX {
		format = "cyclonedx-json"
	}
	_ = format

	// TODO: Load Grype vulnerability database from g.dbPath and match
	// SBOM packages against known vulnerabilities.
	//
	// Key integration points in the Grype library:
	//   1. grype.LoadVulnerabilityDB(cfg, update) — loads the DB from disk
	//   2. format.Decode(reader) — parses an SBOM into a syft SBOM model
	//   3. grype.FindVulnerabilitiesForPackage(store, pkg) — matches packages
	//
	// For now, return empty findings with a stub DB version.
	dbVersion := "stub-v0"
	if g.dbPath != "" {
		dbVersion = "stub-v0:" + strings.TrimSuffix(g.dbPath, "/")
	}

	return nil, ScanMeta{DBVersion: dbVersion}, nil
}
