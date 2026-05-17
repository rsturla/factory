package scan_test

import (
	"context"
	"os"
	"testing"

	"github.com/rsturla/factory/services/scan-service/internal/scan"
)

func TestGrypeScanner_Name(t *testing.T) {
	g := scan.NewGrypeScanner("")
	if got := g.Name(); got != "grype" {
		t.Errorf("Name() = %q, want %q", got, "grype")
	}
}

func TestGrypeScanner_EmptyInput(t *testing.T) {
	g := scan.NewGrypeScanner("")
	_, _, err := g.Scan(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}

	_, _, err = g.Scan(context.Background(), []byte{})
	if err == nil {
		t.Fatal("expected error for zero-length input")
	}
}

func grypeDBPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("GRYPE_DB_DIR")
	if path == "" {
		t.Skip("GRYPE_DB_DIR not set, skipping Grype integration test")
	}
	return path
}

func TestGrypeScanner_Integration_ValidSPDX(t *testing.T) {
	dbPath := grypeDBPath(t)
	g := scan.NewGrypeScanner(dbPath)

	sbom := []byte(`{"spdxVersion":"SPDX-2.3","name":"test","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","documentNamespace":"https://example.com","packages":[]}`)

	findings, meta, err := g.Scan(context.Background(), sbom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = findings
	if meta.DBVersion == "" {
		t.Error("expected non-empty DBVersion")
	}
}

func TestGrypeScanner_Integration_InvalidJSON(t *testing.T) {
	dbPath := grypeDBPath(t)
	g := scan.NewGrypeScanner(dbPath)

	_, _, err := g.Scan(context.Background(), []byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
