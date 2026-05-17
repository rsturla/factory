package scan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rsturla/factory/services/scan-service/internal/scan"
)

func TestGrypeScanner_Name(t *testing.T) {
	g := scan.NewGrypeScanner("")
	if got := g.Name(); got != "grype" {
		t.Errorf("Name() = %q, want %q", got, "grype")
	}
}

func TestGrypeScanner_ValidSPDX(t *testing.T) {
	g := scan.NewGrypeScanner("")
	sbom := []byte(`{"spdxVersion":"SPDX-2.3","name":"test","documentNamespace":"https://example.com"}`)

	findings, meta, err := g.Scan(context.Background(), sbom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings from stub, got %d", len(findings))
	}
	if meta.DBVersion == "" {
		t.Error("expected non-empty DBVersion from stub")
	}
	if !strings.HasPrefix(meta.DBVersion, "stub-v0") {
		t.Errorf("expected DBVersion starting with 'stub-v0', got %q", meta.DBVersion)
	}
}

func TestGrypeScanner_ValidCycloneDX(t *testing.T) {
	g := scan.NewGrypeScanner("")
	sbom := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","version":1}`)

	findings, meta, err := g.Scan(context.Background(), sbom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings from stub, got %d", len(findings))
	}
	if meta.DBVersion == "" {
		t.Error("expected non-empty DBVersion from stub")
	}
}

func TestGrypeScanner_InvalidJSON(t *testing.T) {
	g := scan.NewGrypeScanner("")
	sbom := []byte(`not valid json {{{`)

	_, _, err := g.Scan(context.Background(), sbom)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decode sbom") {
		t.Errorf("expected 'decode sbom' in error, got: %v", err)
	}
}

func TestGrypeScanner_EmptyInput(t *testing.T) {
	g := scan.NewGrypeScanner("")

	_, _, err := g.Scan(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if !strings.Contains(err.Error(), "empty SBOM") {
		t.Errorf("expected 'empty SBOM' in error, got: %v", err)
	}

	// Also test with zero-length slice
	_, _, err = g.Scan(context.Background(), []byte{})
	if err == nil {
		t.Fatal("expected error for zero-length input, got nil")
	}
}

func TestGrypeScanner_UnrecognizedFormat(t *testing.T) {
	g := scan.NewGrypeScanner("")
	sbom := []byte(`{"some":"random","json":"object"}`)

	_, _, err := g.Scan(context.Background(), sbom)
	if err == nil {
		t.Fatal("expected error for unrecognized SBOM format, got nil")
	}
	if !strings.Contains(err.Error(), "unrecognized SBOM format") {
		t.Errorf("expected 'unrecognized SBOM format' in error, got: %v", err)
	}
}

func TestGrypeScanner_DBPathInVersion(t *testing.T) {
	g := scan.NewGrypeScanner("/var/db/grype")
	sbom := []byte(`{"spdxVersion":"SPDX-2.3"}`)

	_, meta, err := g.Scan(context.Background(), sbom)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(meta.DBVersion, "/var/db/grype") {
		t.Errorf("expected DBVersion to contain db path, got %q", meta.DBVersion)
	}
}
