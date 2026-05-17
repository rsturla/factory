package fetch_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/rsturla/factory/services/catalog/internal/fetch"
	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"
)

type mockStore struct {
	platforms map[string]*model.Platform
	images    map[string]*model.Image
}

func newMockStore() *mockStore {
	return &mockStore{
		platforms: make(map[string]*model.Platform),
		images:    make(map[string]*model.Image),
	}
}

func (m *mockStore) UpsertImage(_ context.Context, _ model.Image) error          { return nil }
func (m *mockStore) GetImage(_ context.Context, id string) (*model.Image, error) {
	img, ok := m.images[id]
	if !ok {
		return nil, nil
	}
	return img, nil
}
func (m *mockStore) GetImageByDigest(_ context.Context, _ string) (*model.Image, error) {
	return nil, nil
}
func (m *mockStore) ListImages(_ context.Context, _, _ int) ([]model.Image, int, error) {
	return nil, 0, nil
}
func (m *mockStore) UpsertTag(_ context.Context, _ string, _ model.Tag) error      { return nil }
func (m *mockStore) ListTagsByImage(_ context.Context, _ string) ([]model.Tag, error) {
	return nil, nil
}
func (m *mockStore) GetImageByTag(_ context.Context, _, _, _ string) (*model.Image, error) {
	return nil, nil
}
func (m *mockStore) UpsertPlatform(_ context.Context, _ model.Platform) error { return nil }
func (m *mockStore) GetPlatform(_ context.Context, id string) (*model.Platform, error) {
	p, ok := m.platforms[id]
	if !ok {
		return nil, nil
	}
	return p, nil
}
func (m *mockStore) ListPlatformsByImage(_ context.Context, _ string) ([]model.Platform, error) {
	return nil, nil
}
func (m *mockStore) UpsertPackage(_ context.Context, _ model.Package) (string, error) {
	return "", nil
}
func (m *mockStore) AssociatePackages(_ context.Context, _ string, _ []string) error { return nil }
func (m *mockStore) ListPackagesByPlatform(_ context.Context, _ string) ([]model.Package, error) {
	return nil, nil
}
func (m *mockStore) SearchPackages(_ context.Context, _ string, _ int) ([]model.Package, error) {
	return nil, nil
}
func (m *mockStore) GetImagesByPackage(_ context.Context, _ string) ([]model.Image, error) {
	return nil, nil
}
func (m *mockStore) GetImagesByPackageName(_ context.Context, _, _ string, _ int) ([]model.Image, error) {
	return nil, nil
}
func (m *mockStore) DiffPackages(_ context.Context, _, _ string) ([]model.Package, []model.Package, error) {
	return nil, nil, nil
}
func (m *mockStore) UpsertSBOM(_ context.Context, _ model.SBOM) error          { return nil }
func (m *mockStore) GetSBOM(_ context.Context, _, _ string) (*model.SBOM, error) { return nil, nil }
func (m *mockStore) GetCheckpoint(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockStore) UpdateCheckpoint(_ context.Context, _, _ string) error      { return nil }
func (m *mockStore) Ping(_ context.Context) error                               { return nil }
func (m *mockStore) Close()                                                     {}

var _ store.Store = (*mockStore)(nil)

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

type mockScanner struct {
	result []byte
	err    error
}

func (s *mockScanner) Scan(_ context.Context, _ string) (*fetch.ScanOutput, error) {
	return &fetch.ScanOutput{SBOM: s.result}, s.err
}

func TestFetchReconciler_Completed(t *testing.T) {
	ms := newMockStore()
	ms.platforms["sha256:test123"] = &model.Platform{
		ID:           "sha256:test123",
		ImageID:      "sha256:img",
		OS:           "linux",
		Architecture: "amd64",
	}
	ms.images["sha256:img"] = &model.Image{
		ID:     "sha256:img",
		Digest: "sha256:img",
		Tags:   []model.Tag{{Registry: "quay.io", Repository: "hummingbird/test", Tag: "latest"}},
	}

	blobs := newMockBlobStore()
	scanner := &mockScanner{
		result: []byte(`{"spdxVersion":"SPDX-2.3"}`),
	}

	// Set up a mock receiver server that accepts enqueue requests
	receiverSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer receiverSrv.Close()

	enqueueClient := reconciler.NewEnqueueClient(receiverSrv.URL)
	rec := fetch.NewReconciler(ms, blobs, scanner, enqueueClient, "catalog-analyze")

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key:     "sha256:test123|linux/amd64",
		Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}
	// Verify SBOM was stored to blob store
	if _, ok := blobs.blobs["sboms/test123.spdx.json"]; !ok {
		t.Error("SBOM not stored to blob store")
	}
}

func TestFetchReconciler_ScanError(t *testing.T) {
	ms := newMockStore()
	ms.platforms["sha256:test123"] = &model.Platform{
		ID: "sha256:test123", ImageID: "sha256:img", OS: "linux", Architecture: "amd64",
	}
	ms.images["sha256:img"] = &model.Image{
		ID: "sha256:img", Digest: "sha256:img",
		Tags: []model.Tag{{Registry: "quay.io", Repository: "test/img", Tag: "v1"}},
	}

	blobs := newMockBlobStore()
	scanErr := errors.New("registry timeout")
	scanner := &mockScanner{err: scanErr}

	enqueueClient := reconciler.NewEnqueueClient("http://localhost:9999")
	rec := fetch.NewReconciler(ms, blobs, scanner, enqueueClient, "catalog-analyze")

	_, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key: "sha256:test123|linux/amd64", Attempt: 1,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, scanErr) {
		t.Errorf("error: got %v, want wrapped %v", err, scanErr)
	}
}

func TestFetchReconciler_ManifestNotFound(t *testing.T) {
	ms := newMockStore()
	ms.platforms["sha256:test123"] = &model.Platform{
		ID: "sha256:test123", ImageID: "sha256:img", OS: "linux", Architecture: "amd64",
	}
	ms.images["sha256:img"] = &model.Image{
		ID: "sha256:img", Digest: "sha256:img",
		Tags: []model.Tag{{Registry: "quay.io", Repository: "test/img", Tag: "v1"}},
	}

	blobs := newMockBlobStore()
	scanner := &mockScanner{err: errors.New("MANIFEST_UNKNOWN: not found")}

	enqueueClient := reconciler.NewEnqueueClient("http://localhost:9999")
	rec := fetch.NewReconciler(ms, blobs, scanner, enqueueClient, "catalog-analyze")

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key: "sha256:test123|linux/amd64", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}

func TestFetchReconciler_PlatformNotFound(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	scanner := &mockScanner{}

	enqueueClient := reconciler.NewEnqueueClient("http://localhost:9999")
	rec := fetch.NewReconciler(ms, blobs, scanner, enqueueClient, "catalog-analyze")

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key: "sha256:missing|linux/amd64", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionCompleted {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionCompleted)
	}
}

func TestFetchReconciler_InvalidKey(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()
	scanner := &mockScanner{}

	enqueueClient := reconciler.NewEnqueueClient("http://localhost:9999")
	rec := fetch.NewReconciler(ms, blobs, scanner, enqueueClient, "catalog-analyze")

	resp, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key: "invalid-no-pipe", Attempt: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Action != reconciler.ActionReject {
		t.Errorf("action: got %q, want %q", resp.Action, reconciler.ActionReject)
	}
}
