package scan

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchore/clio"
	"github.com/anchore/grype/grype"
	v6dist "github.com/anchore/grype/grype/db/v6/distribution"
	v6inst "github.com/anchore/grype/grype/db/v6/installation"
	"github.com/anchore/grype/grype/matcher"
	"github.com/anchore/grype/grype/pkg"
	"github.com/anchore/grype/grype/vulnerability"
)

type GrypeScanner struct {
	dbPath string

	mu        sync.RWMutex
	provider  vulnerability.Provider
	dbVersion string
	dbModTime time.Time
}

func NewGrypeScanner(dbPath string) *GrypeScanner {
	return &GrypeScanner{dbPath: dbPath}
}

func (g *GrypeScanner) Name() string { return "grype" }

func (g *GrypeScanner) Scan(ctx context.Context, sbomBytes []byte) ([]Finding, ScanMeta, error) {
	if len(sbomBytes) == 0 {
		return nil, ScanMeta{}, fmt.Errorf("empty SBOM data")
	}

	provider, dbVersion, err := g.getProvider()
	if err != nil {
		return nil, ScanMeta{}, err
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

func (g *GrypeScanner) getProvider() (vulnerability.Provider, string, error) {
	modTime := g.dbFileModTime()

	g.mu.RLock()
	if g.provider != nil && g.dbModTime.Equal(modTime) {
		p, v := g.provider, g.dbVersion
		g.mu.RUnlock()
		return p, v, nil
	}
	g.mu.RUnlock()

	g.mu.Lock()
	defer g.mu.Unlock()

	// Double-check after acquiring write lock
	if g.provider != nil && g.dbModTime.Equal(modTime) {
		return g.provider, g.dbVersion, nil
	}

	slog.Info("loading grype db", "path", g.dbPath, "mod_time", modTime)

	provider, status, err := grype.LoadVulnerabilityDB(
		v6dist.Config{
			ID:                 clio.Identification{Name: "scan-service"},
			RequireUpdateCheck: false,
		},
		v6inst.Config{
			DBRootDir:        g.dbPath,
			ValidateAge:      false,
			ValidateChecksum: false,
		},
		false,
	)
	if err != nil {
		return nil, "", fmt.Errorf("load grype db: %w", err)
	}

	dbVersion := "unknown"
	if status != nil {
		dbVersion = status.SchemaVersion
		if status.Built.String() != "" {
			dbVersion = status.Built.String()
		}
	}

	g.provider = provider
	g.dbVersion = dbVersion
	g.dbModTime = modTime

	slog.Info("grype db loaded", "version", dbVersion)

	return provider, dbVersion, nil
}

func (g *GrypeScanner) dbFileModTime() time.Time {
	entries, err := os.ReadDir(g.dbPath)
	if err != nil {
		return time.Time{}
	}
	var latest time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		full := filepath.Join(g.dbPath, e.Name())
		_ = full
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}
