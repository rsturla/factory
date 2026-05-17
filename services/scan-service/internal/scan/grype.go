package scan

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/anchore/clio"
	"github.com/anchore/grype/grype"
	v6dist "github.com/anchore/grype/grype/db/v6/distribution"
	v6inst "github.com/anchore/grype/grype/db/v6/installation"
	"github.com/anchore/grype/grype/matcher"
	"github.com/anchore/grype/grype/pkg"
)

type GrypeScanner struct {
	dbPath string
}

func NewGrypeScanner(dbPath string) *GrypeScanner {
	return &GrypeScanner{dbPath: dbPath}
}

func (g *GrypeScanner) Name() string { return "grype" }

func (g *GrypeScanner) Scan(ctx context.Context, sbomBytes []byte) ([]Finding, ScanMeta, error) {
	if len(sbomBytes) == 0 {
		return nil, ScanMeta{}, fmt.Errorf("empty SBOM data")
	}

	provider, status, err := grype.LoadVulnerabilityDB(
		v6dist.Config{
			ID:                 clio.Identification{Name: "scan-service"},
			RequireUpdateCheck: false,
		},
		v6inst.Config{
			DBRootDir:      g.dbPath,
			ValidateAge:    false,
			ValidateChecksum: false,
		},
		false,
	)
	if err != nil {
		return nil, ScanMeta{}, fmt.Errorf("load grype db: %w", err)
	}

	dbVersion := "unknown"
	if status != nil {
		dbVersion = status.SchemaVersion
		if status.Built.String() != "" {
			dbVersion = status.Built.String()
		}
	}

	reader := bytes.NewReader(sbomBytes)
	packages, pkgContext, _, err := pkg.ProvideFromReader(reader, pkg.ProviderConfig{
		SynthesisConfig: pkg.SynthesisConfig{
			GenerateMissingCPEs: true,
		},
	})
	if err != nil {
		return nil, ScanMeta{}, fmt.Errorf("parse sbom packages: %w", err)
	}

	matchers := matcher.NewDefaultMatchers(matcher.Config{})

	vm := grype.VulnerabilityMatcher{
		VulnerabilityProvider: provider,
		Matchers:              matchers,
		NormalizeByCVE:        true,
	}

	matches, _, err := vm.FindMatchesContext(ctx, packages, pkgContext)
	if err != nil {
		return nil, ScanMeta{}, fmt.Errorf("find vulnerabilities: %w", err)
	}

	var findings []Finding
	if matches != nil {
		for m := range matches.Enumerate() {
			severity := ""
			if m.Vulnerability.Metadata != nil {
				severity = m.Vulnerability.Metadata.Severity
			}

			fixedVersion := ""
			if len(m.Vulnerability.Fix.Versions) > 0 {
				fixedVersion = strings.Join(m.Vulnerability.Fix.Versions, ", ")
			}

			vulnID := m.Vulnerability.ID
			if vulnID == "" && m.Vulnerability.Metadata != nil {
				vulnID = m.Vulnerability.Metadata.ID
			}

			findings = append(findings, Finding{
				VulnID:         vulnID,
				Severity:       severity,
				PackageName:    m.Package.Name,
				PackageVersion: m.Package.Version,
				PackageType:    string(m.Package.Type),
				FixedVersion:   fixedVersion,
			})
		}
	}

	return findings, ScanMeta{DBVersion: dbVersion}, nil
}
