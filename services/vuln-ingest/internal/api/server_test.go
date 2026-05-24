package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/api"
	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	vulns       map[string]*model.Vulnerability
	kev         map[string]*model.KEVEntry
	epss        map[string]*model.EPSSScore
	checkpoints []*model.SourceCheckpoint
}

func newMockStore() *mockStore {
	return &mockStore{
		vulns: make(map[string]*model.Vulnerability),
		kev:   make(map[string]*model.KEVEntry),
		epss:  make(map[string]*model.EPSSScore),
	}
}

func (m *mockStore) UpsertVulnerability(_ context.Context, v *model.Vulnerability, _ string) error {
	m.vulns[v.ID] = v
	return nil
}

func (m *mockStore) GetVulnerability(_ context.Context, id string) (*model.Vulnerability, error) {
	v, ok := m.vulns[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (m *mockStore) ListVulnerabilities(_ context.Context, opts store.ListOpts) ([]*model.Vulnerability, error) {
	var out []*model.Vulnerability
	for _, v := range m.vulns {
		if opts.ModifiedSince != nil && v.Modified != nil && v.Modified.Before(*opts.ModifiedSince) {
			continue
		}
		out = append(out, v)
	}
	// Apply offset/limit.
	if opts.Offset >= len(out) {
		return nil, nil
	}
	out = out[opts.Offset:]
	if opts.Limit > 0 && opts.Limit < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (m *mockStore) BatchGetVulnerabilities(_ context.Context, ids []string) ([]*model.Vulnerability, error) {
	var out []*model.Vulnerability
	for _, id := range ids {
		if v, ok := m.vulns[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *mockStore) ListAffectedByPackage(_ context.Context, ecosystem, packageName string, opts store.ListOpts) ([]*model.Vulnerability, error) {
	var out []*model.Vulnerability
	for _, v := range m.vulns {
		for _, ap := range v.AffectedPackages {
			if ap.Ecosystem == ecosystem && ap.PackageName == packageName {
				out = append(out, v)
				break
			}
		}
	}
	if opts.Offset >= len(out) {
		return nil, nil
	}
	out = out[opts.Offset:]
	if opts.Limit > 0 && opts.Limit < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (m *mockStore) UpsertSourceRecord(_ context.Context, _ *model.SourceRecord) error { return nil }
func (m *mockStore) GetSourceRecord(_ context.Context, _, _ string) (*model.SourceRecord, error) {
	return nil, nil
}

func (m *mockStore) GetCheckpoint(_ context.Context, _ string) (*model.SourceCheckpoint, error) {
	return nil, nil
}
func (m *mockStore) UpdateCheckpoint(_ context.Context, _, _ string, _ int64) error { return nil }
func (m *mockStore) SetCheckpointError(_ context.Context, _, _ string) error       { return nil }
func (m *mockStore) ListCheckpoints(_ context.Context) ([]*model.SourceCheckpoint, error) {
	return m.checkpoints, nil
}

func (m *mockStore) UpsertKEVEntries(_ context.Context, entries []model.KEVEntry) error {
	for i := range entries {
		m.kev[entries[i].CVEID] = &entries[i]
	}
	return nil
}
func (m *mockStore) GetKEVEntry(_ context.Context, cveID string) (*model.KEVEntry, error) {
	e, ok := m.kev[cveID]
	if !ok {
		return nil, nil
	}
	return e, nil
}

func (m *mockStore) UpsertEPSSScores(_ context.Context, scores []model.EPSSScore) error {
	for i := range scores {
		m.epss[scores[i].CVEID] = &scores[i]
	}
	return nil
}
func (m *mockStore) GetEPSSScore(_ context.Context, cveID string) (*model.EPSSScore, error) {
	e, ok := m.epss[cveID]
	if !ok {
		return nil, nil
	}
	return e, nil
}

func (m *mockStore) GetAllEPSSScoreMap(_ context.Context) (map[string]float32, error) {
	out := make(map[string]float32)
	for k, v := range m.epss {
		out[k] = v.Score
	}
	return out, nil
}
func (m *mockStore) GetAllKEVIDs(_ context.Context) (map[string]time.Time, error) {
	out := make(map[string]time.Time)
	for k, v := range m.kev {
		if v.DateAdded != nil {
			out[k] = *v.DateAdded
		}
	}
	return out, nil
}

func (m *mockStore) ListAffectedByPurl(_ context.Context, purl string, opts store.ListOpts) ([]*model.Vulnerability, error) {
	var ids []string
	for id, v := range m.vulns {
		for _, ap := range v.AffectedPackages {
			if ap.Purl == purl {
				ids = append(ids, id)
				break
			}
		}
	}
	return m.BatchGetVulnerabilities(context.Background(), ids)
}

func (m *mockStore) BatchQueryAffected(_ context.Context, queries []store.AffectedQuery, opts store.ListOpts) (map[string][]*model.Vulnerability, error) {
	return map[string][]*model.Vulnerability{}, nil
}

func (m *mockStore) CountVulnerabilities(_ context.Context, _ store.ListOpts) (int, error) {
	return len(m.vulns), nil
}

func (m *mockStore) CountAffectedByPackage(_ context.Context, eco, pkg string) (int, error) {
	count := 0
	for _, v := range m.vulns {
		for _, ap := range v.AffectedPackages {
			if ap.Ecosystem == eco && ap.PackageName == pkg {
				count++
				break
			}
		}
	}
	return count, nil
}

func (m *mockStore) GetRelatedVulnerabilities(_ context.Context, _ string) ([]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) UpsertVendorNotes(_ context.Context, _ []model.VendorNote) error {
	return nil
}
func (m *mockStore) GetVendorNotes(_ context.Context, _ string) ([]model.VendorNote, error) {
	return nil, nil
}
func (m *mockStore) GetVendorNoteCVEIDs(_ context.Context, _ string) (map[string]time.Time, error) {
	return nil, nil
}

func (m *mockStore) Ping(_ context.Context) error { return nil }
func (m *mockStore) Close()                        {}

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

func doRequest(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
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

func seedVuln(t *testing.T, ms *mockStore, id string) *model.Vulnerability {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	v := &model.Vulnerability{
		ID:      id,
		Summary: "Test vulnerability " + id,
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "Go", PackageName: "example.com/lib"},
		},
		Modified: &now,
	}
	ms.vulns[id] = v
	return v
}

// ---------------------------------------------------------------------------
// GET /v1/vulns/{id}
// ---------------------------------------------------------------------------

func TestGetVulnerability_Valid(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-1234")

	rec := doRequest(t, mux, "GET", "/v1/vulns/CVE-2024-1234", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["id"] != "CVE-2024-1234" {
		t.Fatalf("id = %v, want CVE-2024-1234", resp["id"])
	}
}

func TestGetVulnerability_NotFound(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns/CVE-9999-0000", "")
	assertStatus(t, rec, http.StatusNotFound)
}

func TestGetVulnerability_WithKEVAndEPSS(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-5678")

	dateAdded := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC)
	ms.kev["CVE-2024-5678"] = &model.KEVEntry{
		CVEID:         "CVE-2024-5678",
		VendorProject: "acme",
		Product:       "widget",
		DateAdded:     &dateAdded,
	}
	ms.epss["CVE-2024-5678"] = &model.EPSSScore{
		CVEID:      "CVE-2024-5678",
		Score:      0.95,
		Percentile: 0.99,
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns/CVE-2024-5678", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	if resp["kev"] == nil {
		t.Fatal("expected kev enrichment, got nil")
	}
	kev := resp["kev"].(map[string]any)
	if kev["vendor_project"] != "acme" {
		t.Fatalf("kev.vendor_project = %v, want acme", kev["vendor_project"])
	}

	if resp["epss"] == nil {
		t.Fatal("expected epss enrichment, got nil")
	}
	epss := resp["epss"].(map[string]any)
	score := epss["score"].(float64)
	if score < 0.9 {
		t.Fatalf("epss.score = %v, want >= 0.9", score)
	}
}

func TestGetVulnerability_NonCVEWithAlias(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now().UTC()
	ms.vulns["GHSA-xxxx-yyyy-zzzz"] = &model.Vulnerability{
		ID:       "GHSA-xxxx-yyyy-zzzz",
		Summary:  "GHSA advisory",
		Aliases:  []string{"CVE-2024-9999"},
		Modified: &now,
	}
	ms.epss["CVE-2024-9999"] = &model.EPSSScore{
		CVEID:      "CVE-2024-9999",
		Score:      0.42,
		Percentile: 0.88,
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns/GHSA-xxxx-yyyy-zzzz", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	if resp["epss"] == nil {
		t.Fatal("expected epss enrichment via CVE alias, got nil")
	}
	epss := resp["epss"].(map[string]any)
	if epss["cve_id"] != "CVE-2024-9999" {
		t.Fatalf("epss.cve_id = %v, want CVE-2024-9999", epss["cve_id"])
	}
}

// ---------------------------------------------------------------------------
// GET /v1/vulns
// ---------------------------------------------------------------------------

func TestListVulnerabilities_Default(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001")
	seedVuln(t, ms, "CVE-2024-0002")

	rec := doRequest(t, mux, "GET", "/v1/vulns", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	total := resp["total"].(float64)

	if len(vulns) != 2 {
		t.Fatalf("got %d vulns, want 2", len(vulns))
	}
	if int(total) != 2 {
		t.Fatalf("total = %v, want 2", total)
	}
}

func TestListVulnerabilities_CustomPagination(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	for i := 0; i < 5; i++ {
		seedVuln(t, ms, fmt.Sprintf("CVE-2024-%04d", i))
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=2&offset=0", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 2 {
		t.Fatalf("got %d vulns, want 2", len(vulns))
	}
}

func TestListVulnerabilities_ModifiedSince(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	old := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	ms.vulns["CVE-2023-0001"] = &model.Vulnerability{
		ID: "CVE-2023-0001", Modified: &old,
	}
	ms.vulns["CVE-2025-0001"] = &model.Vulnerability{
		ID: "CVE-2025-0001", Modified: &recent,
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns?modified_since=2025-01-01T00:00:00Z", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1 (only recent)", len(vulns))
	}
}

func TestListVulnerabilities_ModifiedSinceInvalid(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns?modified_since=not-a-date", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestListVulnerabilities_Empty(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	// When store returns nil, resp["vulnerabilities"] will be JSON null.
	// Either null or empty array is acceptable; total must be 0.
	total := resp["total"].(float64)
	if int(total) != 0 {
		t.Fatalf("total = %v, want 0", total)
	}
}

// ---------------------------------------------------------------------------
// POST /v1/vulns:batchGet
// ---------------------------------------------------------------------------

func TestBatchGet_Valid(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001")
	seedVuln(t, ms, "CVE-2024-0002")

	body := `{"ids":["CVE-2024-0001","CVE-2024-0002"]}`
	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", body)
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 2 {
		t.Fatalf("got %d vulns, want 2", len(vulns))
	}
}

func TestBatchGet_EmptyIDs(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", `{"ids":[]}`)
	assertStatus(t, rec, http.StatusOK)
}

func TestBatchGet_TooManyIDs(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ids := make([]string, 1001)
	for i := range ids {
		ids[i] = fmt.Sprintf("CVE-2024-%04d", i)
	}
	payload, _ := json.Marshal(map[string]any{"ids": ids})

	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", string(payload))
	assertStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "max 1000 ids") {
		t.Fatalf("expected 'max 1000 ids' in body, got: %s", rec.Body.String())
	}
}

func TestBatchGet_MalformedJSON(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", `{not json`)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBatchGet_BodyTooLarge(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	// Build a body larger than 1MB. The MaxBytesReader limit is 1<<20 = 1048576.
	// We use a valid-ish JSON prefix with a very long string value.
	big := `{"ids":["` + strings.Repeat("A", 1<<20+100) + `"]}`
	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", big)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBatchGet_MixedExistingAndMissing(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001")

	body := `{"ids":["CVE-2024-0001","CVE-2024-NOPE"]}`
	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", body)
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1 (only existing)", len(vulns))
	}
}

// ---------------------------------------------------------------------------
// GET /v1/affected
// ---------------------------------------------------------------------------

func TestListAffected_Valid(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001") // seeded with ecosystem=Go, package_name=example.com/lib

	rec := doRequest(t, mux, "GET", "/v1/affected?ecosystem=Go&package_name=example.com/lib", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1", len(vulns))
	}
}

func TestListAffected_EcosystemOptional(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/affected?package_name=example.com/lib", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestListAffected_MissingBothParams(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/affected", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestListAffected_CustomPagination(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	for i := 0; i < 5; i++ {
		now := time.Now().UTC()
		ms.vulns[fmt.Sprintf("CVE-2024-%04d", i)] = &model.Vulnerability{
			ID:       fmt.Sprintf("CVE-2024-%04d", i),
			Modified: &now,
			AffectedPackages: []model.AffectedPackage{
				{Ecosystem: "npm", PackageName: "leftpad"},
			},
		}
	}

	rec := doRequest(t, mux, "GET", "/v1/affected?ecosystem=npm&package_name=leftpad&limit=2&offset=0", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 2 {
		t.Fatalf("got %d vulns, want 2", len(vulns))
	}
}

// ---------------------------------------------------------------------------
// GET /v1/sources
// ---------------------------------------------------------------------------

func TestGetSourceStatus(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now().UTC().Truncate(time.Second)
	ms.checkpoints = []*model.SourceCheckpoint{
		{Source: "osv", CheckpointValue: "2024-01-01", LastSyncAt: now, ItemsSynced: 500},
		{Source: "nvd", CheckpointValue: "2024-06-15", LastSyncAt: now, ItemsSynced: 1200},
	}

	rec := doRequest(t, mux, "GET", "/v1/sources", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	sources, ok := resp["sources"].([]any)
	if !ok {
		t.Fatalf("sources is not array: %T", resp["sources"])
	}
	if len(sources) != 2 {
		t.Fatalf("got %d sources, want 2", len(sources))
	}
}

func TestGetSourceStatus_Empty(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/sources", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	// nil slice encodes as JSON null, so sources may be null.
	total := resp["sources"]
	if total != nil {
		sources := total.([]any)
		if len(sources) != 0 {
			t.Fatalf("got %d sources, want 0", len(sources))
		}
	}
}

// ---------------------------------------------------------------------------
// intParam tests (exercised through HTTP endpoints)
// ---------------------------------------------------------------------------

func TestIntParam_ValidValue(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	// limit=50 should work. We just verify the endpoint returns 200.
	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=50", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestIntParam_InvalidFallsToDefault(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=notanumber", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestIntParam_NegativeFallsToDefault(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=-5", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestIntParam_LimitCappedAt1000(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	// Seed 1 item so we can verify the response succeeds.
	seedVuln(t, ms, "CVE-2024-0001")

	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=5000", "")
	assertStatus(t, rec, http.StatusOK)

	// The request should succeed with the capped limit, not error out.
	var resp map[string]any
	decodeJSON(t, rec, &resp)
}

func TestIntParam_MissingParamUsesDefault(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	// No limit param at all — should use default (100).
	rec := doRequest(t, mux, "GET", "/v1/vulns", "")
	assertStatus(t, rec, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Edge-case: GET /v1/vulns/{id} with no enrichment (non-CVE, no aliases)
// ---------------------------------------------------------------------------

func TestGetVulnerability_NonCVENoAlias_NoEnrichment(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now().UTC()
	ms.vulns["GHSA-aaaa-bbbb-cccc"] = &model.Vulnerability{
		ID:       "GHSA-aaaa-bbbb-cccc",
		Summary:  "GHSA advisory with no CVE alias",
		Modified: &now,
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns/GHSA-aaaa-bbbb-cccc", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)

	if resp["kev"] != nil {
		t.Fatal("expected nil kev for non-CVE vuln without CVE alias")
	}
	if resp["epss"] != nil {
		t.Fatal("expected nil epss for non-CVE vuln without CVE alias")
	}
}

// ---------------------------------------------------------------------------
// Verify JSON Content-Type on batch endpoint
// ---------------------------------------------------------------------------

func TestBatchGet_ContentType(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "POST", "/v1/vulns:batchGet", `{"ids":[]}`)
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")
}

// ---------------------------------------------------------------------------
// POST /v1/affected:batchQuery
// ---------------------------------------------------------------------------

func TestBatchQueryAffected_Valid(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001") // ecosystem=Go, package_name=example.com/lib

	body := `{"queries":[{"ecosystem":"Go","package_name":"example.com/lib"}]}`
	rec := doRequest(t, mux, "POST", "/v1/affected:batchQuery", body)
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["results"] == nil {
		t.Fatal("expected results key in response")
	}
}

func TestBatchQueryAffected_TooManyQueries(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	queries := make([]map[string]string, 501)
	for i := range queries {
		queries[i] = map[string]string{
			"ecosystem":    "Go",
			"package_name": fmt.Sprintf("example.com/pkg%d", i),
		}
	}
	payload, _ := json.Marshal(map[string]any{"queries": queries})

	rec := doRequest(t, mux, "POST", "/v1/affected:batchQuery", string(payload))
	assertStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "max 500 queries") {
		t.Fatalf("expected 'max 500 queries' in body, got: %s", rec.Body.String())
	}
}

func TestBatchQueryAffected_MalformedJSON(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "POST", "/v1/affected:batchQuery", `{not valid json`)
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// GET /v1/affected?purl=...
// ---------------------------------------------------------------------------

func TestListAffected_ByPurl(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now().UTC()
	ms.vulns["CVE-2024-PURL"] = &model.Vulnerability{
		ID:       "CVE-2024-PURL",
		Summary:  "purl-based vuln",
		Modified: &now,
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "npm", PackageName: "express", Purl: "pkg:npm/express"},
		},
	}

	rec := doRequest(t, mux, "GET", "/v1/affected?purl=pkg:npm/express", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1", len(vulns))
	}
}

// ---------------------------------------------------------------------------
// GET /v1/vulns?updated_since=...
// ---------------------------------------------------------------------------

func TestListVulnerabilities_UpdatedSince(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-0001")

	rec := doRequest(t, mux, "GET", "/v1/vulns?updated_since=2020-01-01T00:00:00Z", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")
}

func TestListVulnerabilities_InvalidUpdatedSince(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/v1/vulns?updated_since=not-a-date", "")
	assertStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "invalid updated_since") {
		t.Fatalf("expected 'invalid updated_since' in body, got: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// enrich=false
// ---------------------------------------------------------------------------

func TestListVulnerabilities_EnrichFalse(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedVuln(t, ms, "CVE-2024-ENRICH")

	dateAdded := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ms.kev["CVE-2024-ENRICH"] = &model.KEVEntry{
		CVEID:         "CVE-2024-ENRICH",
		VendorProject: "TestVendor",
		Product:       "TestProduct",
		DateAdded:     &dateAdded,
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns?enrich=false", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1", len(vulns))
	}
	v := vulns[0].(map[string]any)
	if v["kev"] != nil {
		t.Fatal("expected no kev field when enrich=false")
	}
}

func TestListAffected_EnrichFalse(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	now := time.Now().UTC()
	ms.vulns["CVE-2024-AFF-ENR"] = &model.Vulnerability{
		ID:       "CVE-2024-AFF-ENR",
		Summary:  "affected enrich test",
		Modified: &now,
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "Go", PackageName: "example.com/enrichtest"},
		},
	}
	dateAdded := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	ms.kev["CVE-2024-AFF-ENR"] = &model.KEVEntry{
		CVEID:         "CVE-2024-AFF-ENR",
		VendorProject: "TestVendor",
		Product:       "TestProduct",
		DateAdded:     &dateAdded,
	}

	rec := doRequest(t, mux, "GET", "/v1/affected?ecosystem=Go&package_name=example.com/enrichtest&enrich=false", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	if len(vulns) != 1 {
		t.Fatalf("got %d vulns, want 1", len(vulns))
	}
	v := vulns[0].(map[string]any)
	if v["kev"] != nil {
		t.Fatal("expected no kev field when enrich=false on affected endpoint")
	}
}

// ---------------------------------------------------------------------------
// Total count accuracy
// ---------------------------------------------------------------------------

func TestListVulnerabilities_TotalCount(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	for i := 0; i < 5; i++ {
		seedVuln(t, ms, fmt.Sprintf("CVE-2024-TC%02d", i))
	}

	rec := doRequest(t, mux, "GET", "/v1/vulns?limit=2&offset=0", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	vulns := resp["vulnerabilities"].([]any)
	total := int(resp["total"].(float64))

	if len(vulns) != 2 {
		t.Fatalf("got %d vulns in page, want 2", len(vulns))
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5 (the true count, not page size)", total)
	}
}
