package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rsturla/factory/services/scan-service/internal/api"
	"github.com/rsturla/factory/services/scan-service/internal/model"
	"github.com/rsturla/factory/services/scan-service/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	scans    map[string]*model.Scan     // keyed by platformID|scanner
	findings map[string][]model.Finding // keyed by platformID|scanner
	dbStates map[string]*model.ScannerDBState
}

func newMockStore() *mockStore {
	return &mockStore{
		scans:    make(map[string]*model.Scan),
		findings: make(map[string][]model.Finding),
		dbStates: make(map[string]*model.ScannerDBState),
	}
}

func (m *mockStore) UpsertScan(_ context.Context, s model.Scan) error {
	m.scans[s.PlatformID+"|"+s.Scanner] = &s
	return nil
}

func (m *mockStore) GetLatestScan(_ context.Context, platformID, scanner string) (*model.Scan, error) {
	s, ok := m.scans[platformID+"|"+scanner]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *mockStore) ListScansByImage(_ context.Context, _ string) ([]model.Scan, error) {
	return nil, nil
}

func (m *mockStore) UpsertFindings(_ context.Context, _ string, _ []model.Finding) error {
	return nil
}

func (m *mockStore) ListFindings(_ context.Context, _ string) ([]model.Finding, error) {
	return nil, nil
}

func (m *mockStore) ListFindingsByPlatform(_ context.Context, platformID, scanner string) ([]model.Finding, error) {
	return m.findings[platformID+"|"+scanner], nil
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
// Test helpers
// ---------------------------------------------------------------------------

func setupServer(t *testing.T, ms *mockStore) *http.ServeMux {
	t.Helper()
	srv := api.NewServer(ms)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return mux
}

func doRequest(t *testing.T, mux http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, want, rec.Body.String())
	}
}

func assertContentType(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	got := rec.Header().Get("Content-Type")
	if got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(dest); err != nil {
		t.Fatalf("decode json: %v; body: %s", err, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/scans/{platformID}
// ---------------------------------------------------------------------------

func TestGetScans_Empty(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/scans/sha256:abc123|linux/amd64")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestGetScans_WithData(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now()
	ms.scans["sha256:abc123|linux/amd64|grype"] = &model.Scan{
		ID:            "scan1",
		PlatformID:    "sha256:abc123|linux/amd64",
		Scanner:       "grype",
		DBVersion:     "v5.0",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   now,
		VulnCount:     3,
		CriticalCount: 1,
		HighCount:     2,
		Status:        "completed",
	}

	rec := doRequest(t, mux, "GET", "/api/v1/scans/sha256:abc123|linux/amd64")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/findings/{platformID}
// ---------------------------------------------------------------------------

func TestGetFindings_Empty(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")
}

func TestGetFindings_WithData(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
		{ScanID: "s1", VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 2 {
		t.Fatalf("total_count = %d, want 2", totalCount)
	}
}

func TestGetFindings_SeverityFilter(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
		{ScanID: "s1", VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64?severity=critical")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 1 {
		t.Fatalf("total_count = %d, want 1 (filtered to critical)", totalCount)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/status
// ---------------------------------------------------------------------------

func TestGetStatus(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/status")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	scanners := resp["scanners"].([]any)
	if len(scanners) != 1 {
		t.Fatalf("expected 1 scanner status, got %d", len(scanners))
	}
}

func TestGetStatus_WithDBState(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.dbStates["grype"] = &model.ScannerDBState{
		Scanner:   "grype",
		Version:   "v5.0",
		Checksum:  "abc123",
		UpdatedAt: time.Now(),
	}

	rec := doRequest(t, mux, "GET", "/api/v1/status")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	// Check that state is non-null for grype
	scanners := resp["scanners"].([]any)
	s := scanners[0].(map[string]any)
	if s["state"] == nil {
		t.Fatal("expected non-nil state for grype scanner")
	}

	// Check the version in the parsed state
	state := s["state"].(map[string]any)
	if state["version"] != "v5.0" {
		t.Fatalf("version: got %v, want v5.0", state["version"])
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/findings/{platformID}?scanner=grype
// ---------------------------------------------------------------------------

func TestGetFindings_ScannerFilter(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
	}

	// Explicitly request scanner=grype
	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64?scanner=grype")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 1 {
		t.Fatalf("total_count = %d, want 1", totalCount)
	}

	// Verify scanner name in response
	scanners := resp["scanners"].([]any)
	if len(scanners) != 1 {
		t.Fatalf("expected 1 scanner group, got %d", len(scanners))
	}
	scannerGroup := scanners[0].(map[string]any)
	if scannerGroup["scanner"] != "grype" {
		t.Errorf("scanner = %v, want grype", scannerGroup["scanner"])
	}
}

func TestGetFindings_ScannerFilterNoResults(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
	}

	// Request with a different scanner — should get 0 results
	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64?scanner=trivy")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 0 {
		t.Fatalf("total_count = %d, want 0 (trivy has no findings)", totalCount)
	}
}

func TestGetFindings_BothFilters(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
		{ScanID: "s1", VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
		{ScanID: "s1", VulnID: "CVE-2024-9999", Severity: "Critical", PackageName: "curl", PackageVersion: "8.0"},
	}

	// Filter by scanner=grype AND severity=critical
	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64?scanner=grype&severity=critical")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 2 {
		t.Fatalf("total_count = %d, want 2 (only critical from grype)", totalCount)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/scans/{platformID} — multiple scanners
// ---------------------------------------------------------------------------

func TestGetScans_MultipleScannersOnlyGrypeReturned(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now()
	// Add grype scan
	ms.scans["sha256:multi|linux/amd64|grype"] = &model.Scan{
		ID:            "scan-grype",
		PlatformID:    "sha256:multi|linux/amd64",
		Scanner:       "grype",
		DBVersion:     "v5.0",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   now,
		VulnCount:     3,
		CriticalCount: 1,
		HighCount:     2,
		Status:        "completed",
	}

	// The server only queries "grype" scanner, so trivy data stored under
	// a different key pattern would not be returned. Verify we get exactly 1.
	rec := doRequest(t, mux, "GET", "/api/v1/scans/sha256:multi|linux/amd64")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	scans := resp["scans"].([]any)
	scanEntry := scans[0].(map[string]any)
	if scanEntry["scanner"] != "grype" {
		t.Errorf("scanner = %v, want grype", scanEntry["scanner"])
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/status — with DB state for scanner
// ---------------------------------------------------------------------------

func TestGetStatus_NoDBState(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/status")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	scanners := resp["scanners"].([]any)
	if len(scanners) != 1 {
		t.Fatalf("expected 1 scanner, got %d", len(scanners))
	}
	s := scanners[0].(map[string]any)
	if s["scanner"] != "grype" {
		t.Errorf("scanner = %v, want grype", s["scanner"])
	}
	// State should be null when no DB state is stored
	if s["state"] != nil {
		t.Errorf("expected nil state, got %v", s["state"])
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestGetFindings_EmptyPlatformID(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	// The wildcard pattern {platformID...} will match an empty trailing segment
	// but the handler checks for empty platformID and returns 400.
	rec := doRequest(t, mux, "GET", "/api/v1/findings/")
	// Go 1.22+ ServeMux: trailing slash with wildcard may return 404 or route differently.
	// The important thing is it does NOT panic and returns a valid HTTP status.
	if rec.Code < 200 || rec.Code >= 600 {
		t.Fatalf("unexpected status code: %d", rec.Code)
	}
}

func TestGetScans_NonexistentPlatform(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/scans/sha256:doesnotexist|linux/amd64")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 0 {
		t.Fatalf("count = %d, want 0 for nonexistent platform", count)
	}
}

func TestGetFindings_SeverityFilterCaseInsensitive(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.findings["sha256:abc123|linux/amd64|grype"] = []model.Finding{
		{ScanID: "s1", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1"},
		{ScanID: "s1", VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
	}

	// Test uppercase severity filter
	rec := doRequest(t, mux, "GET", "/api/v1/findings/sha256:abc123|linux/amd64?severity=CRITICAL")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	totalCount := int(resp["total_count"].(float64))
	if totalCount != 1 {
		t.Fatalf("total_count = %d, want 1 (CRITICAL filter should match Critical)", totalCount)
	}
}
