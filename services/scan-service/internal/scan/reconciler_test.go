package scan_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/rsturla/factory/services/scan-service/internal/model"
	"github.com/rsturla/factory/services/scan-service/internal/scan"
	"github.com/rsturla/factory/services/scan-service/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	scans    map[string]model.Scan
	findings map[string][]model.Finding
	dbStates map[string]*model.ScannerDBState
}

func newMockStore() *mockStore {
	return &mockStore{
		scans:    make(map[string]model.Scan),
		findings: make(map[string][]model.Finding),
		dbStates: make(map[string]*model.ScannerDBState),
	}
}

func (m *mockStore) UpsertScan(_ context.Context, s model.Scan) error {
	m.scans[s.ID] = s
	return nil
}

func (m *mockStore) GetLatestScan(_ context.Context, platformID, scanner string) (*model.Scan, error) {
	for _, s := range m.scans {
		if s.PlatformID == platformID && s.Scanner == scanner {
			return &s, nil
		}
	}
	return nil, nil
}

func (m *mockStore) ListScansByImage(_ context.Context, _ string) ([]model.Scan, error) {
	return nil, nil
}

func (m *mockStore) UpsertFindings(_ context.Context, scanID string, findings []model.Finding) error {
	m.findings[scanID] = findings
	return nil
}

func (m *mockStore) ListFindings(_ context.Context, scanID string) ([]model.Finding, error) {
	return m.findings[scanID], nil
}

func (m *mockStore) ListFindingsByPlatform(_ context.Context, _, _ string) ([]model.Finding, error) {
	return nil, nil
}

func (m *mockStore) GetDBState(_ context.Context, scanner string) (*model.ScannerDBState, error) {
	return m.dbStates[scanner], nil
}

func (m *mockStore) UpsertDBState(_ context.Context, state model.ScannerDBState) error {
	m.dbStates[state.Scanner] = &state
	return nil
}

func (m *mockStore) Ping(_ context.Context) error { return nil }
func (m *mockStore) Close()                       {}

var _ store.Store = (*mockStore)(nil)

// ---------------------------------------------------------------------------
// Mock blob store
// ---------------------------------------------------------------------------

type mockBlobStore struct {
	blobs map[string][]byte
}

func newMockBlobStore() *mockBlobStore {
	return &mockBlobStore{blobs: make(map[string][]byte)}
}

func (b *mockBlobStore) Put(_ context.Context, key string, data []byte) error {
	b.blobs[key] = data
	return nil
}

func (b *mockBlobStore) Get(_ context.Context, key string) ([]byte, error) {
	data, ok := b.blobs[key]
	if !ok {
		return nil, fmt.Errorf("blob: not found")
	}
	return data, nil
}

func (b *mockBlobStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := b.blobs[key]
	return ok, nil
}

// ---------------------------------------------------------------------------
// Mock scanner
// ---------------------------------------------------------------------------

type mockScanner struct {
	name     string
	findings []scan.Finding
	meta     scan.ScanMeta
	err      error
}

func (s *mockScanner) Name() string { return s.name }
func (s *mockScanner) Scan(_ context.Context, _ []byte) ([]scan.Finding, scan.ScanMeta, error) {
	return s.findings, s.meta, s.err
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReconciler_SuccessfulScan(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/abc123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name: "grype",
		findings: []scan.Finding{
			{VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1", PackageType: "rpm", FixedVersion: "3.0.2"},
			{VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38", PackageType: "rpm"},
		},
		meta: scan.ScanMeta{DBVersion: "v5.0"},
	}

	scanners := map[string]scan.Scanner{"grype": scanner}
	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:abc123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}

	// Verify scan was stored
	if len(ms.scans) != 1 {
		t.Fatalf("expected 1 scan, got %d", len(ms.scans))
	}
	for _, s := range ms.scans {
		if s.VulnCount != 2 {
			t.Errorf("vuln_count: got %d, want 2", s.VulnCount)
		}
		if s.CriticalCount != 1 {
			t.Errorf("critical_count: got %d, want 1", s.CriticalCount)
		}
		if s.HighCount != 1 {
			t.Errorf("high_count: got %d, want 1", s.HighCount)
		}
		if s.Scanner != "grype" {
			t.Errorf("scanner: got %q, want %q", s.Scanner, "grype")
		}
		if s.DBVersion != "v5.0" {
			t.Errorf("db_version: got %q, want %q", s.DBVersion, "v5.0")
		}
	}

	// Verify findings were stored
	if len(ms.findings) != 1 {
		t.Fatalf("expected findings for 1 scan, got %d", len(ms.findings))
	}
	for _, findings := range ms.findings {
		if len(findings) != 2 {
			t.Fatalf("expected 2 findings, got %d", len(findings))
		}
	}
}

func TestReconciler_UnknownScanner(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	scanners := map[string]scan.Scanner{}

	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "unknown|sha256:abc123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}

func TestReconciler_MissingSBOM(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	// No SBOM stored in blob store
	scanner := &mockScanner{name: "grype"}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:missing|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}

func TestReconciler_ScannerError_Retries(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/abc123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name: "grype",
		err:  errors.New("database connection timeout"),
	}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)

	_, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:abc123|linux/amd64",
		Attempt: 1,
	})
	if err == nil {
		t.Fatal("expected error for retriable scanner failure")
	}
}

func TestReconciler_ScannerNotFoundError_Rejects(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/abc123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name: "grype",
		err:  errors.New("vulnerability not found in database"),
	}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:abc123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}

func TestReconciler_InvalidKey(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	scanners := map[string]scan.Scanner{}

	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "no-pipe-separator",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}

func TestReconciler_EmptyFindings(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/clean123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name:     "grype",
		findings: nil,
		meta:     scan.ScanMeta{DBVersion: "v5.0"},
	}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:clean123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}

	// Verify scan was stored with zero counts
	if len(ms.scans) != 1 {
		t.Fatalf("expected 1 scan, got %d", len(ms.scans))
	}
	for _, s := range ms.scans {
		if s.VulnCount != 0 {
			t.Errorf("vuln_count: got %d, want 0", s.VulnCount)
		}
	}

	// No findings should have been stored
	if len(ms.findings) != 0 {
		t.Errorf("expected 0 finding entries, got %d", len(ms.findings))
	}
}

func TestReconciler_SeverityCountsFromFindings(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/sev123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name: "grype",
		findings: []scan.Finding{
			{VulnID: "CVE-2024-0001", Severity: "Critical", PackageName: "libssl", PackageVersion: "1.0"},
			{VulnID: "CVE-2024-0002", Severity: "Critical", PackageName: "libcrypto", PackageVersion: "1.0"},
			{VulnID: "CVE-2024-0003", Severity: "High", PackageName: "curl", PackageVersion: "8.0"},
			{VulnID: "CVE-2024-0004", Severity: "Medium", PackageName: "zlib", PackageVersion: "1.3"},
			{VulnID: "CVE-2024-0005", Severity: "Medium", PackageName: "bzip2", PackageVersion: "1.0"},
			{VulnID: "CVE-2024-0006", Severity: "Medium", PackageName: "xz", PackageVersion: "5.4"},
			{VulnID: "CVE-2024-0007", Severity: "Low", PackageName: "file", PackageVersion: "5.45"},
		},
		meta: scan.ScanMeta{DBVersion: "v6.0"},
	}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)
	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:sev123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}

	if len(ms.scans) != 1 {
		t.Fatalf("expected 1 scan, got %d", len(ms.scans))
	}
	for _, s := range ms.scans {
		if s.VulnCount != 7 {
			t.Errorf("vuln_count: got %d, want 7", s.VulnCount)
		}
		if s.CriticalCount != 2 {
			t.Errorf("critical_count: got %d, want 2", s.CriticalCount)
		}
		if s.HighCount != 1 {
			t.Errorf("high_count: got %d, want 1", s.HighCount)
		}
		if s.MediumCount != 3 {
			t.Errorf("medium_count: got %d, want 3", s.MediumCount)
		}
		if s.LowCount != 1 {
			t.Errorf("low_count: got %d, want 1", s.LowCount)
		}
	}
}

func TestReconciler_AllSeveritiesStored(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/allsev.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	scanner := &mockScanner{
		name: "grype",
		findings: []scan.Finding{
			{VulnID: "CVE-2024-0010", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0"},
			{VulnID: "CVE-2024-0011", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
			{VulnID: "CVE-2024-0012", Severity: "Medium", PackageName: "zlib", PackageVersion: "1.3"},
			{VulnID: "CVE-2024-0013", Severity: "Low", PackageName: "file", PackageVersion: "5.45"},
			{VulnID: "CVE-2024-0014", Severity: "Negligible", PackageName: "tar", PackageVersion: "1.35"},
		},
		meta: scan.ScanMeta{DBVersion: "v6.0"},
	}
	scanners := map[string]scan.Scanner{"grype": scanner}

	rec := scan.NewReconciler(ms, blobs, scanners)
	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:allsev|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}

	// All 5 findings should be stored
	for _, findings := range ms.findings {
		if len(findings) != 5 {
			t.Fatalf("expected 5 findings stored, got %d", len(findings))
		}
		// Verify each severity is present
		severities := make(map[string]bool)
		for _, f := range findings {
			severities[f.Severity] = true
		}
		for _, sev := range []string{"Critical", "High", "Medium", "Low", "Negligible"} {
			if !severities[sev] {
				t.Errorf("missing severity %q in stored findings", sev)
			}
		}
	}

	// VulnCount should be 5, but "Negligible" doesn't increment any named counter
	for _, s := range ms.scans {
		if s.VulnCount != 5 {
			t.Errorf("vuln_count: got %d, want 5", s.VulnCount)
		}
		if s.CriticalCount != 1 {
			t.Errorf("critical_count: got %d, want 1", s.CriticalCount)
		}
		if s.HighCount != 1 {
			t.Errorf("high_count: got %d, want 1", s.HighCount)
		}
		if s.MediumCount != 1 {
			t.Errorf("medium_count: got %d, want 1", s.MediumCount)
		}
		if s.LowCount != 1 {
			t.Errorf("low_count: got %d, want 1", s.LowCount)
		}
	}
}

func TestReconciler_ConcurrentScannerRegistration(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	blobs.blobs["sboms/multi123.spdx.json"] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

	grypeScanner := &mockScanner{
		name: "grype",
		findings: []scan.Finding{
			{VulnID: "CVE-2024-1000", Severity: "High", PackageName: "openssl", PackageVersion: "3.0.1"},
		},
		meta: scan.ScanMeta{DBVersion: "grype-v5"},
	}

	trivyScanner := &mockScanner{
		name: "trivy",
		findings: []scan.Finding{
			{VulnID: "CVE-2024-1000", Severity: "High", PackageName: "openssl", PackageVersion: "3.0.1"},
			{VulnID: "CVE-2024-2000", Severity: "Critical", PackageName: "glibc", PackageVersion: "2.38"},
		},
		meta: scan.ScanMeta{DBVersion: "trivy-v2"},
	}

	scanners := map[string]scan.Scanner{
		"grype": grypeScanner,
		"trivy": trivyScanner,
	}
	rec := scan.NewReconciler(ms, blobs, scanners)

	// Dispatch to grype
	resp1, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "grype|sha256:multi123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("grype scan error: %v", err)
	}
	if resp1.Action != reconciler.ActionCompleted {
		t.Errorf("grype action: got %q, want %q", resp1.Action, reconciler.ActionCompleted)
	}

	// Dispatch to trivy
	resp2, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "trivy|sha256:multi123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("trivy scan error: %v", err)
	}
	if resp2.Action != reconciler.ActionCompleted {
		t.Errorf("trivy action: got %q, want %q", resp2.Action, reconciler.ActionCompleted)
	}

	// Should have 2 scans — one per scanner
	if len(ms.scans) != 2 {
		t.Fatalf("expected 2 scans, got %d", len(ms.scans))
	}

	// Verify correct dispatch: each scan has the right scanner name and DB version
	var foundGrype, foundTrivy bool
	for _, s := range ms.scans {
		switch s.Scanner {
		case "grype":
			foundGrype = true
			if s.DBVersion != "grype-v5" {
				t.Errorf("grype db_version: got %q, want %q", s.DBVersion, "grype-v5")
			}
			if s.VulnCount != 1 {
				t.Errorf("grype vuln_count: got %d, want 1", s.VulnCount)
			}
		case "trivy":
			foundTrivy = true
			if s.DBVersion != "trivy-v2" {
				t.Errorf("trivy db_version: got %q, want %q", s.DBVersion, "trivy-v2")
			}
			if s.VulnCount != 2 {
				t.Errorf("trivy vuln_count: got %d, want 2", s.VulnCount)
			}
		default:
			t.Errorf("unexpected scanner: %q", s.Scanner)
		}
	}
	if !foundGrype {
		t.Error("grype scan not found")
	}
	if !foundTrivy {
		t.Error("trivy scan not found")
	}

	// Verify findings were stored separately
	if len(ms.findings) != 2 {
		t.Fatalf("expected findings for 2 scans, got %d", len(ms.findings))
	}
}

func TestReconciler_SBOMBlobKeyFormats(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		wantBlob string
	}{
		{
			name:     "standard sha256 digest",
			key:      "grype|sha256:abc123def456|linux/amd64",
			wantBlob: "sboms/abc123def456.spdx.json",
		},
		{
			name:     "sha256 with long hex",
			key:      "grype|sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855|linux/arm64",
			wantBlob: "sboms/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855.spdx.json",
		},
		{
			name:     "platform with variant (arm/v8)",
			key:      "grype|sha256:arm8digest|linux/arm64/v8",
			wantBlob: "sboms/arm8digest.spdx.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := newMockStore()
			blobs := newMockBlobStore()
			// Seed the expected blob key
			blobs.blobs[tt.wantBlob] = []byte(`{"spdxVersion":"SPDX-2.3"}`)

			scanner := &mockScanner{
				name:     "grype",
				findings: nil,
				meta:     scan.ScanMeta{DBVersion: "v1"},
			}
			scanners := map[string]scan.Scanner{"grype": scanner}
			rec := scan.NewReconciler(ms, blobs, scanners)

			resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
				Key:     tt.key,
				Attempt: 1,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.Action != reconciler.ActionCompleted {
				t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
			}

			// If we got here with ActionCompleted, the blob key was constructed correctly
			if len(ms.scans) != 1 {
				t.Fatalf("expected 1 scan, got %d", len(ms.scans))
			}
		})
	}
}
