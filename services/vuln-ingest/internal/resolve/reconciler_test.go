package resolve_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/resolve"
	"github.com/hummingbird-org/vuln-ingest/internal/resolve/parser"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	vulns         map[string]*model.Vulnerability
	sourceRecords map[string]*model.SourceRecord
	kevEntries    []model.KEVEntry
	epssScores    []model.EPSSScore

	upsertVulnErr error
	upsertSRErr   error
	upsertKEVErr  error
	upsertEPSSErr error
}

func newMockStore() *mockStore {
	return &mockStore{
		vulns:         make(map[string]*model.Vulnerability),
		sourceRecords: make(map[string]*model.SourceRecord),
	}
}

func (m *mockStore) UpsertVulnerability(_ context.Context, v *model.Vulnerability, _ string) error {
	if m.upsertVulnErr != nil {
		return m.upsertVulnErr
	}
	m.vulns[v.ID] = v
	return nil
}

func (m *mockStore) GetSourceRecord(_ context.Context, vulnID, source string) (*model.SourceRecord, error) {
	key := vulnID + "|" + source
	rec, ok := m.sourceRecords[key]
	if !ok {
		return nil, nil
	}
	return rec, nil
}

func (m *mockStore) UpsertSourceRecord(_ context.Context, rec *model.SourceRecord) error {
	if m.upsertSRErr != nil {
		return m.upsertSRErr
	}
	key := rec.VulnID + "|" + rec.Source
	m.sourceRecords[key] = rec
	return nil
}

func (m *mockStore) UpsertKEVEntries(_ context.Context, entries []model.KEVEntry) error {
	if m.upsertKEVErr != nil {
		return m.upsertKEVErr
	}
	m.kevEntries = append(m.kevEntries, entries...)
	return nil
}

func (m *mockStore) UpsertEPSSScores(_ context.Context, scores []model.EPSSScore) error {
	if m.upsertEPSSErr != nil {
		return m.upsertEPSSErr
	}
	m.epssScores = append(m.epssScores, scores...)
	return nil
}

// --- stubs for the rest of the Store interface ---

func (m *mockStore) GetVulnerability(context.Context, string) (*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) ListVulnerabilities(context.Context, store.ListOpts) ([]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) BatchGetVulnerabilities(context.Context, []string) ([]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) ListAffectedByPackage(context.Context, string, string, store.ListOpts) ([]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) GetCheckpoint(context.Context, string) (*model.SourceCheckpoint, error) {
	return nil, nil
}
func (m *mockStore) UpdateCheckpoint(context.Context, string, string, int64) error { return nil }
func (m *mockStore) SetCheckpointError(context.Context, string, string) error      { return nil }
func (m *mockStore) ListCheckpoints(context.Context) ([]*model.SourceCheckpoint, error) {
	return nil, nil
}
func (m *mockStore) GetKEVEntry(context.Context, string) (*model.KEVEntry, error) { return nil, nil }
func (m *mockStore) GetEPSSScore(context.Context, string) (*model.EPSSScore, error) {
	return nil, nil
}
func (m *mockStore) GetAllEPSSScoreMap(context.Context) (map[string]float32, error) {
	return nil, nil
}
func (m *mockStore) GetAllKEVIDs(context.Context) (map[string]time.Time, error) { return nil, nil }
func (m *mockStore) UpsertVendorNotes(_ context.Context, _ []model.VendorNote) error {
	return nil
}
func (m *mockStore) GetVendorNotes(context.Context, string) ([]model.VendorNote, error) {
	return nil, nil
}
func (m *mockStore) GetVendorNoteCVEIDs(context.Context, string) (map[string]time.Time, error) {
	return nil, nil
}
func (m *mockStore) ListAffectedByPurl(context.Context, string, store.ListOpts) ([]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) BatchQueryAffected(context.Context, []store.AffectedQuery, store.ListOpts) (map[string][]*model.Vulnerability, error) {
	return nil, nil
}
func (m *mockStore) CountVulnerabilities(context.Context, store.ListOpts) (int, error) { return 0, nil }
func (m *mockStore) CountAffectedByPackage(context.Context, string, string) (int, error) {
	return 0, nil
}
func (m *mockStore) GetRelatedVulnerabilities(_ context.Context, _ string) ([]*model.Vulnerability, error) {
	return nil, nil
}

func (m *mockStore) Ping(context.Context) error { return nil }
func (m *mockStore) Close()                     {}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testBlobs(t *testing.T) blob.Store {
	t.Helper()
	b, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeBlob(t *testing.T, blobs blob.Store, key string, data []byte) {
	t.Helper()
	if err := blobs.Put(context.Background(), key, data); err != nil {
		t.Fatal(err)
	}
}

func makeReq(key string) reconciler.ProcessRequest {
	return reconciler.ProcessRequest{Key: key, Attempt: 1}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReconcile_ValidOSVFile(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	osv := map[string]any{
		"id":       "GHSA-1234-5678-9012",
		"summary":  "Test vulnerability",
		"modified": "2024-01-15T10:00:00Z",
		"affected": []map[string]any{
			{
				"package": map[string]string{
					"ecosystem": "Go",
					"name":      "example.com/pkg",
				},
				"ranges": []map[string]any{
					{
						"type":   "SEMVER",
						"events": []map[string]string{{"introduced": "0"}, {"fixed": "1.2.3"}},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(osv)
	writeBlob(t, blobs, "ghsa/advisories/GHSA-1234.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/advisories/GHSA-1234.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	if _, ok := s.vulns["GHSA-1234-5678-9012"]; !ok {
		t.Fatal("vulnerability not upserted")
	}
	if len(s.sourceRecords) != 1 {
		t.Fatalf("expected 1 source record, got %d", len(s.sourceRecords))
	}
}

func TestReconcile_ValidNVDFile(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	nvd := map[string]any{
		"id":           "CVE-2024-1234",
		"published":    "2024-01-10T12:00:00.000",
		"lastModified": "2024-01-15T12:00:00.000",
		"vulnStatus":   "Analyzed",
		"descriptions": []map[string]string{
			{"lang": "en", "value": "A test NVD vulnerability for unit testing purposes."},
		},
		"metrics":        map[string]any{},
		"configurations": []any{},
		"references":     []any{},
		"weaknesses":     []any{},
	}
	data, _ := json.Marshal(nvd)
	writeBlob(t, blobs, "nvd/cves/CVE-2024-1234.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("nvd/cves/CVE-2024-1234.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	v, ok := s.vulns["CVE-2024-1234"]
	if !ok {
		t.Fatal("vulnerability not upserted")
	}
	if v.Summary == "" {
		t.Fatal("expected summary to be set from NVD description")
	}
}

func TestReconcile_FileNotFound(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/advisories/nonexistent.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed for missing file (idempotent), got %s", resp.Action)
	}
}

func TestReconcile_UnknownSourcePrefix(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	writeBlob(t, blobs, "unknown/some-file.json", []byte(`{"id":"X"}`))

	resp, err := r.Reconcile(context.Background(), makeReq("unknown/some-file.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Fatalf("expected reject for unknown source, got %s", resp.Action)
	}
}

func TestReconcile_PathTraversal(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	cases := []string{
		"../../etc/passwd",
		"../escape",
		"foo/../../../bar",
		"/absolute/path.json",
		"foo/..%2f..%2fetc/passwd",
	}

	for _, key := range cases {
		resp, err := r.Reconcile(context.Background(), makeReq(key))
		if err != nil {
			t.Fatalf("key %q: unexpected error: %v", key, err)
		}
		if resp.Action != reconciler.ActionReject {
			t.Fatalf("key %q: expected reject, got %s", key, resp.Action)
		}
	}
}

func TestReconcile_UnchangedHash(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	osv := map[string]any{
		"id":       "GHSA-UNCHANGED",
		"summary":  "already processed",
		"modified": "2024-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(osv)
	writeBlob(t, blobs, "ghsa/unchanged.json", data)

	// First reconcile: populates store.
	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/unchanged.json"))
	if err != nil {
		t.Fatalf("first reconcile error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("first reconcile: expected completed, got %s", resp.Action)
	}
	if len(s.vulns) != 1 {
		t.Fatalf("expected 1 vuln after first reconcile, got %d", len(s.vulns))
	}

	// Reset vulns map to detect whether upsert is called again.
	s.vulns = make(map[string]*model.Vulnerability)

	// Second reconcile with same data: hash unchanged, should skip upsert.
	resp, err = r.Reconcile(context.Background(), makeReq("ghsa/unchanged.json"))
	if err != nil {
		t.Fatalf("second reconcile error: %v", err)
	}
	if resp.Action != reconciler.ActionConverged {
		t.Fatalf("second reconcile: expected converged, got %s", resp.Action)
	}
	if len(s.vulns) != 0 {
		t.Fatal("expected upsert to be skipped for unchanged hash")
	}
}

func TestReconcile_ParserError(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	writeBlob(t, blobs, "ghsa/bad.json", []byte(`{not valid json!!!`))

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/bad.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Fatalf("expected reject for malformed JSON, got %s", resp.Action)
	}
}

func TestReconcile_KEVBatch(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	kev := map[string]any{
		"entries": []map[string]string{
			{
				"cveID":            "CVE-2024-0001",
				"vendorProject":    "TestVendor",
				"product":          "TestProduct",
				"dateAdded":        "2024-01-01",
				"shortDescription": "Test KEV entry",
				"requiredAction":   "Apply update",
				"dueDate":          "2024-02-01",
			},
			{
				"cveID":            "CVE-2024-0002",
				"vendorProject":    "OtherVendor",
				"product":          "OtherProduct",
				"dateAdded":        "2024-01-05",
				"shortDescription": "Another KEV entry",
				"requiredAction":   "Mitigate",
				"dueDate":          "2024-02-15",
			},
		},
	}
	data, _ := json.Marshal(kev)
	writeBlob(t, blobs, "kev/batch-2024-01-15.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("kev/batch-2024-01-15.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	if len(s.kevEntries) != 2 {
		t.Fatalf("expected 2 KEV entries, got %d", len(s.kevEntries))
	}
	if s.kevEntries[0].CVEID != "CVE-2024-0001" {
		t.Fatalf("expected CVE-2024-0001, got %s", s.kevEntries[0].CVEID)
	}
}

func TestReconcile_EPSSBatch(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	epss := map[string]any{
		"model_version": "v2024.01.01",
		"score_date":    "2024-01-15",
		"scores": []map[string]any{
			{"cve": "CVE-2024-1111", "epss": 0.05, "percentile": 0.75},
			{"cve": "CVE-2024-2222", "epss": 0.95, "percentile": 0.99},
		},
	}
	data, _ := json.Marshal(epss)
	writeBlob(t, blobs, "epss/batch-2024-01-15.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("epss/batch-2024-01-15.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	if len(s.epssScores) != 2 {
		t.Fatalf("expected 2 EPSS scores, got %d", len(s.epssScores))
	}
	if s.epssScores[0].CVEID != "CVE-2024-1111" {
		t.Fatalf("expected CVE-2024-1111, got %s", s.epssScores[0].CVEID)
	}
}

func TestReconcile_RegisterParser(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	// Register a custom parser that always returns a fixed vulnerability.
	r.RegisterParser("custom", &stubParser{
		vulns: []model.Vulnerability{{ID: "CUSTOM-001", Summary: "custom parsed"}},
	})

	writeBlob(t, blobs, "custom/entry.json", []byte(`{"anything":"goes"}`))

	resp, err := r.Reconcile(context.Background(), makeReq("custom/entry.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	v, ok := s.vulns["CUSTOM-001"]
	if !ok {
		t.Fatal("custom parser vuln not upserted")
	}
	if v.Summary != "custom parsed" {
		t.Fatalf("expected 'custom parsed', got %q", v.Summary)
	}
}

func TestReconcile_UpsertVulnerabilityError(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	s.upsertVulnErr = errors.New("db connection lost")
	r := resolve.NewReconciler(s, blobs)

	osv := map[string]any{
		"id":       "GHSA-ERR-TEST",
		"summary":  "will fail on upsert",
		"modified": "2024-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(osv)
	writeBlob(t, blobs, "ghsa/err.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/err.json"))
	if err == nil {
		t.Fatal("expected error from UpsertVulnerability, got nil")
	}
	// Should NOT be a reject -- it's a retriable error.
	if resp.Action == reconciler.ActionReject {
		t.Fatal("expected retriable error, not reject")
	}
}

func TestReconcile_UpsertSourceRecordError(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	s.upsertSRErr = errors.New("sr write failed")
	r := resolve.NewReconciler(s, blobs)

	osv := map[string]any{
		"id":       "GHSA-SR-FAIL",
		"summary":  "will fail on source record upsert",
		"modified": "2024-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(osv)
	writeBlob(t, blobs, "ghsa/sr-fail.json", data)

	_, err := r.Reconcile(context.Background(), makeReq("ghsa/sr-fail.json"))
	if err == nil {
		t.Fatal("expected error from UpsertSourceRecord, got nil")
	}
}

func TestReconcile_UpsertKEVEntriesError(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	s.upsertKEVErr = errors.New("kev write failed")
	r := resolve.NewReconciler(s, blobs)

	kev := map[string]any{
		"entries": []map[string]string{
			{"cveID": "CVE-2024-9999", "vendorProject": "V", "product": "P", "dateAdded": "2024-01-01", "shortDescription": "d", "requiredAction": "a", "dueDate": "2024-02-01"},
		},
	}
	data, _ := json.Marshal(kev)
	writeBlob(t, blobs, "kev/batch-err.json", data)

	_, err := r.Reconcile(context.Background(), makeReq("kev/batch-err.json"))
	if err == nil {
		t.Fatal("expected error from UpsertKEVEntries, got nil")
	}
}

func TestReconcile_UpsertEPSSScoresError(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	s.upsertEPSSErr = errors.New("epss write failed")
	r := resolve.NewReconciler(s, blobs)

	epss := map[string]any{
		"model_version": "v1", "score_date": "2024-01-01",
		"scores": []map[string]any{{"cve": "CVE-2024-8888", "epss": 0.5, "percentile": 0.9}},
	}
	data, _ := json.Marshal(epss)
	writeBlob(t, blobs, "epss/batch-err.json", data)

	_, err := r.Reconcile(context.Background(), makeReq("epss/batch-err.json"))
	if err == nil {
		t.Fatal("expected error from UpsertEPSSScores, got nil")
	}
}

// ---------------------------------------------------------------------------
// Stub parser for RegisterParser test
// ---------------------------------------------------------------------------

type stubParser struct {
	vulns []model.Vulnerability
	err   error
}

func (p *stubParser) Parse([]byte) ([]model.Vulnerability, error) {
	return p.vulns, p.err
}

// Compile-time check: stubParser implements parser.Parser.
var _ parser.Parser = (*stubParser)(nil)

// ---------------------------------------------------------------------------
// Source-tracking mock store
// ---------------------------------------------------------------------------

type sourceTrackingStore struct {
	mockStore
	upsertCalls []sourceTrackingCall
}

type sourceTrackingCall struct {
	VulnID string
	Source string
}

func newSourceTrackingStore() *sourceTrackingStore {
	return &sourceTrackingStore{
		mockStore: mockStore{
			vulns:         make(map[string]*model.Vulnerability),
			sourceRecords: make(map[string]*model.SourceRecord),
		},
	}
}

func (s *sourceTrackingStore) UpsertVulnerability(ctx context.Context, v *model.Vulnerability, source string) error {
	s.upsertCalls = append(s.upsertCalls, sourceTrackingCall{VulnID: v.ID, Source: source})
	return s.mockStore.UpsertVulnerability(ctx, v, source)
}

// ---------------------------------------------------------------------------
// TestReconcile_SourceParamPassed
// ---------------------------------------------------------------------------

func TestReconcile_SourceParamPassed(t *testing.T) {
	blobs := testBlobs(t)
	s := newSourceTrackingStore()
	r := resolve.NewReconciler(s, blobs)

	osv := map[string]any{
		"id":       "GHSA-SRC-TEST-0001",
		"summary":  "source param test",
		"modified": "2024-06-01T00:00:00Z",
	}
	data, _ := json.Marshal(osv)
	writeBlob(t, blobs, "ghsa/source-test.json", data)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/source-test.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}

	if len(s.upsertCalls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(s.upsertCalls))
	}
	call := s.upsertCalls[0]
	if call.Source != "ghsa" {
		t.Errorf("source: got %q, want %q", call.Source, "ghsa")
	}
	if call.VulnID != "GHSA-SRC-TEST-0001" {
		t.Errorf("vuln id: got %q, want GHSA-SRC-TEST-0001", call.VulnID)
	}
}

// ---------------------------------------------------------------------------
// Fuzz: key validation rejects traversal attempts
// ---------------------------------------------------------------------------

func FuzzReconcile_KeyValidation(f *testing.F) {
	f.Add("ghsa/advisory.json")
	f.Add("../../etc/passwd")
	f.Add("../escape")
	f.Add("/absolute")
	f.Add("foo/../../../bar")
	f.Add("normal/deep/key.json")
	f.Add("")
	f.Add("nvd/CVE-2024-0001.json")

	f.Fuzz(func(t *testing.T, key string) {
		blobs := testBlobs(t)
		s := newMockStore()
		r := resolve.NewReconciler(s, blobs)

		resp, err := r.Reconcile(context.Background(), reconciler.ProcessRequest{Key: key, Attempt: 1})

		hasDotDot := strings.Contains(key, "..")
		hasLeadingSlash := strings.HasPrefix(key, "/")

		if hasDotDot || hasLeadingSlash {
			if err != nil {
				t.Skipf("error is acceptable for malicious key: %v", err)
			}
			if resp.Action != reconciler.ActionReject {
				t.Errorf("key %q contains traversal but was not rejected (action=%s)", key, resp.Action)
			}
		}
		// Valid keys may complete, reject (unknown source), or return not-found — all OK.
		_ = err
	})
}

// ---------------------------------------------------------------------------
// TestReconcile_LeadingSlashRejected
// ---------------------------------------------------------------------------

func TestReconcile_DoubleDotInFilename_Rejected(t *testing.T) {
	// Keys containing ".." anywhere are rejected as defense-in-depth,
	// even if technically valid (e.g., "foo..bar"). This is intentional.
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	writeBlob(t, blobs, "ghsa/foo..bar.json", []byte(`{"id":"X"}`))

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa/foo..bar.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Fatalf("expected reject for key with '..', got %s", resp.Action)
	}
}

func TestReconcile_LeadingSlashRejected(t *testing.T) {
	blobs := testBlobs(t)
	s := newMockStore()
	r := resolve.NewReconciler(s, blobs)

	resp, err := r.Reconcile(context.Background(), makeReq("/etc/shadow"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Fatalf("expected reject for leading slash, got %s", resp.Action)
	}
}
