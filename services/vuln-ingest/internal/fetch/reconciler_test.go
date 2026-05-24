package fetch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	mu            sync.Mutex
	checkpoints   map[string]*model.SourceCheckpoint
	lastError     map[string]string
	lastItemCount map[string]int64
}

func newMockStore() *mockStore {
	return &mockStore{
		checkpoints:   make(map[string]*model.SourceCheckpoint),
		lastError:     make(map[string]string),
		lastItemCount: make(map[string]int64),
	}
}

func (m *mockStore) GetCheckpoint(_ context.Context, source string) (*model.SourceCheckpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, ok := m.checkpoints[source]
	if !ok {
		return nil, nil
	}
	return cp, nil
}

func (m *mockStore) UpdateCheckpoint(_ context.Context, src, value string, items int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkpoints[src] = &model.SourceCheckpoint{
		Source:          src,
		CheckpointValue: value,
		ItemsSynced:     items,
		LastSyncAt:      time.Now(),
	}
	m.lastItemCount[src] = items
	// Clear error on successful update.
	delete(m.lastError, src)
	return nil
}

func (m *mockStore) SetCheckpointError(_ context.Context, src, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastError[src] = errMsg
	return nil
}

// --- stubs for the rest of the Store interface ---

func (m *mockStore) UpsertVulnerability(context.Context, *model.Vulnerability, string) error {
	return nil
}
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
func (m *mockStore) UpsertSourceRecord(context.Context, *model.SourceRecord) error { return nil }
func (m *mockStore) GetSourceRecord(context.Context, string, string) (*model.SourceRecord, error) {
	return nil, nil
}
func (m *mockStore) ListCheckpoints(context.Context) ([]*model.SourceCheckpoint, error) {
	return nil, nil
}
func (m *mockStore) UpsertKEVEntries(context.Context, []model.KEVEntry) error    { return nil }
func (m *mockStore) GetKEVEntry(context.Context, string) (*model.KEVEntry, error) { return nil, nil }
func (m *mockStore) UpsertEPSSScores(context.Context, []model.EPSSScore) error   { return nil }
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
// Mock source
// ---------------------------------------------------------------------------

type mockSource struct {
	name   string
	result source.FetchResult
	err    error

	// calledCheckpoint records the checkpoint value passed to Fetch.
	calledCheckpoint string
}

func (s *mockSource) Name() string { return s.name }

func (s *mockSource) Fetch(_ context.Context, _ blob.Store, checkpoint string) (source.FetchResult, error) {
	s.calledCheckpoint = checkpoint
	return s.result, s.err
}

// ---------------------------------------------------------------------------
// Receiver mock (httptest)
// ---------------------------------------------------------------------------

type receiverLog struct {
	mu       sync.Mutex
	calls    int
	payloads []json.RawMessage
	status   int // response status; 200 if zero
}

func newReceiver(t *testing.T, log *receiverLog) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/enqueue/batch" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.mu.Lock()
		log.calls++
		log.payloads = append(log.payloads, json.RawMessage(body))
		status := log.status
		log.mu.Unlock()

		if status != 0 {
			http.Error(w, "simulated error", status)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeReq(key string) reconciler.ProcessRequest {
	return reconciler.ProcessRequest{Key: key, Attempt: 1}
}

func testBlobs(t *testing.T) blob.Store {
	t.Helper()
	b, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func makeReconciler(t *testing.T, s *mockStore, src *mockSource, receiverURL string) *fetch.Reconciler {
	t.Helper()
	r := fetch.NewReconciler(s, testBlobs(t), receiverURL, "vuln-resolve")
	r.RegisterSource(src)
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReconcile_UnknownSourceKey(t *testing.T) {
	s := newMockStore()
	r := fetch.NewReconciler(s, testBlobs(t), "http://unused", "vuln-resolve")
	// No sources registered.

	resp, err := r.Reconcile(context.Background(), makeReq("nonexistent"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Fatalf("expected reject for unknown source, got %s", resp.Action)
	}
}

func TestReconcile_SourceReturnsNoChanges(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "ghsa",
		result: source.FetchResult{
			NewCheckpoint: "abc123",
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionConverged {
		t.Fatalf("expected converged, got %s", resp.Action)
	}
	if rlog.calls != 0 {
		t.Fatalf("expected 0 HTTP calls, got %d", rlog.calls)
	}
}

func TestReconcile_SourceReturnsChanges(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "ghsa",
		result: source.FetchResult{
			ChangedFiles:  []string{"ghsa/file1.json", "ghsa/file2.json"},
			NewCheckpoint: "def456",
			ItemCount:     2,
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}
	if rlog.calls != 1 {
		t.Fatalf("expected 1 HTTP call, got %d", rlog.calls)
	}

	// Verify payload structure.
	var payload struct {
		Queue string `json:"queue"`
		Items []struct {
			Key      string `json:"key"`
			Priority int    `json:"priority"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rlog.payloads[0], &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Queue != "vuln-resolve" {
		t.Fatalf("expected queue 'vuln-resolve', got %q", payload.Queue)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(payload.Items))
	}
}

func TestReconcile_FirstSync_NoCheckpoint(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "nvd",
		result: source.FetchResult{
			ChangedFiles:  []string{"nvd/cve-1.json"},
			NewCheckpoint: "first-cp",
			ItemCount:     1,
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("nvd"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.calledCheckpoint != "" {
		t.Fatalf("expected empty checkpoint for first sync, got %q", src.calledCheckpoint)
	}
}

func TestReconcile_ExistingCheckpoint(t *testing.T) {
	s := newMockStore()
	s.checkpoints["osv"] = &model.SourceCheckpoint{
		Source:          "osv",
		CheckpointValue: "existing-cp-val",
	}

	src := &mockSource{
		name: "osv",
		result: source.FetchResult{
			ChangedFiles:  []string{"osv/a.json"},
			NewCheckpoint: "new-cp",
			ItemCount:     1,
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("osv"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if src.calledCheckpoint != "existing-cp-val" {
		t.Fatalf("expected checkpoint 'existing-cp-val', got %q", src.calledCheckpoint)
	}
}

func TestReconcile_SourceFetchError(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "ghsa",
		err:  errors.New("network timeout"),
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("ghsa"))
	if err == nil {
		t.Fatal("expected error from source.Fetch, got nil")
	}
	// Should have set checkpoint error.
	s.mu.Lock()
	errMsg := s.lastError["ghsa"]
	s.mu.Unlock()
	if errMsg == "" {
		t.Fatal("expected checkpoint error to be set")
	}
}

func TestReconcile_BatchEnqueueError(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "ghsa",
		result: source.FetchResult{
			ChangedFiles:  []string{"ghsa/file1.json"},
			NewCheckpoint: "cp-abc",
			ItemCount:     1,
		},
	}
	rlog := &receiverLog{status: http.StatusInternalServerError}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("ghsa"))
	if err == nil {
		t.Fatal("expected error from batch enqueue, got nil")
	}
}

func TestReconcile_LargeBatch_SplitsHTTPCalls(t *testing.T) {
	s := newMockStore()

	// Generate 5001+4999 = 10000 keys to trigger >1 batch.
	keys := make([]string, 10001)
	for i := range keys {
		keys[i] = "ghsa/file-" + string(rune('A'+i%26)) + ".json"
	}

	src := &mockSource{
		name: "ghsa",
		result: source.FetchResult{
			ChangedFiles:  keys,
			NewCheckpoint: "cp-large",
			ItemCount:     len(keys),
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	resp, err := r.Reconcile(context.Background(), makeReq("ghsa"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Fatalf("expected completed, got %s", resp.Action)
	}

	// 10001 keys / MaxBatchSize (5000) = 3 batches.
	expectedBatches := (len(keys) + fetch.MaxBatchSize - 1) / fetch.MaxBatchSize
	if rlog.calls != expectedBatches {
		t.Fatalf("expected %d HTTP calls for %d keys (batch size %d), got %d",
			expectedBatches, len(keys), fetch.MaxBatchSize, rlog.calls)
	}
}

func TestReconcile_CheckpointUpdated(t *testing.T) {
	s := newMockStore()
	src := &mockSource{
		name: "kev",
		result: source.FetchResult{
			ChangedFiles:  []string{"kev/batch.json"},
			NewCheckpoint: "v2024.01.20",
			ItemCount:     5,
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("kev"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s.mu.Lock()
	cp := s.checkpoints["kev"]
	s.mu.Unlock()

	if cp == nil {
		t.Fatal("checkpoint not updated")
	}
	if cp.CheckpointValue != "v2024.01.20" {
		t.Fatalf("expected checkpoint 'v2024.01.20', got %q", cp.CheckpointValue)
	}
}

func TestReconcile_CheckpointErrorCleared(t *testing.T) {
	s := newMockStore()
	// Pre-set an error from a previous failed sync.
	s.lastError["epss"] = "previous failure"

	src := &mockSource{
		name: "epss",
		result: source.FetchResult{
			ChangedFiles:  []string{"epss/batch.json"},
			NewCheckpoint: "2024-01-20",
			ItemCount:     100,
		},
	}
	rlog := &receiverLog{}
	srv := newReceiver(t, rlog)
	defer srv.Close()

	r := makeReconciler(t, s, src, srv.URL)

	_, err := r.Reconcile(context.Background(), makeReq("epss"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s.mu.Lock()
	errMsg := s.lastError["epss"]
	s.mu.Unlock()

	if errMsg != "" {
		t.Fatalf("expected checkpoint error to be cleared, got %q", errMsg)
	}
}
