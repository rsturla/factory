package analyze_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/rsturla/factory/services/catalog/internal/analyze"
	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"
)

type mockStore struct {
	platforms map[string]*model.Platform
	images    map[string]*model.Image
	packages  []string
	sbomSaved bool
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
func (m *mockStore) UpsertPlatform(_ context.Context, p model.Platform) error {
	m.platforms[p.ID] = &p
	return nil
}
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
func (m *mockStore) UpsertPackage(_ context.Context, pkg model.Package) (string, error) {
	id := "pkg-" + pkg.Name
	m.packages = append(m.packages, id)
	return id, nil
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
func (m *mockStore) UpsertSBOM(_ context.Context, _ model.SBOM) error {
	m.sbomSaved = true
	return nil
}
func (m *mockStore) GetSBOM(_ context.Context, _, _ string) (*model.SBOM, error) {
	return nil, nil
}
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

// minimalSPDXJSON is a minimal valid SPDX JSON document that Syft's format.Decode can parse.
// It contains two packages for testing extraction.
var minimalSPDXJSON = []byte(`{
	"spdxVersion": "SPDX-2.3",
	"dataLicense": "CC0-1.0",
	"SPDXID": "SPDXRef-DOCUMENT",
	"name": "test",
	"documentNamespace": "https://example.com/test",
	"creationInfo": {
		"created": "2024-01-01T00:00:00Z",
		"creators": ["Tool: test"]
	},
	"packages": [
		{
			"SPDXID": "SPDXRef-Package-rpm-glibc",
			"name": "glibc",
			"versionInfo": "2.39",
			"downloadLocation": "NOASSERTION",
			"externalRefs": [
				{
					"referenceCategory": "PACKAGE-MANAGER",
					"referenceType": "purl",
					"referenceLocator": "pkg:rpm/redhat/glibc@2.39"
				}
			]
		},
		{
			"SPDXID": "SPDXRef-Package-go-module-net",
			"name": "golang.org/x/net",
			"versionInfo": "0.45.0",
			"downloadLocation": "NOASSERTION",
			"externalRefs": [
				{
					"referenceCategory": "PACKAGE-MANAGER",
					"referenceType": "purl",
					"referenceLocator": "pkg:golang/golang.org/x/net@0.45.0"
				}
			]
		}
	]
}`)

func TestAnalyzeReconciler_Completed(t *testing.T) {
	ms := newMockStore()
	ms.platforms["sha256:test123"] = &model.Platform{
		ID:           "sha256:test123",
		ImageID:      "sha256:img",
		OS:           "linux",
		Architecture: "amd64",
	}

	blobs := newMockBlobStore()
	// The blob key strips the "sha256:" prefix
	blobs.blobs["sboms/test123.spdx.json"] = minimalSPDXJSON

	rec := analyze.NewReconciler(ms, blobs)
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
	if len(ms.packages) != 2 {
		t.Errorf("packages upserted: got %d, want 2", len(ms.packages))
	}
	if !ms.sbomSaved {
		t.Error("SBOM not saved")
	}
}

func TestAnalyzeReconciler_BlobNotFound(t *testing.T) {
	ms := newMockStore()
	ms.platforms["sha256:test123"] = &model.Platform{
		ID: "sha256:test123", ImageID: "sha256:img", OS: "linux", Architecture: "amd64",
	}

	blobs := newMockBlobStore()
	// Don't put any SBOM — simulate missing blob

	rec := analyze.NewReconciler(ms, blobs)
	_, err := rec.Reconcile(context.Background(), reconciler.ProcessRequest{
		Key: "sha256:test123|linux/amd64", Attempt: 1,
	})
	if err == nil {
		t.Fatal("expected error for missing blob")
	}
}

func TestAnalyzeReconciler_PlatformNotFound(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()

	rec := analyze.NewReconciler(ms, blobs)
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

func TestAnalyzeReconciler_InvalidKey(t *testing.T) {
	ms := newMockStore()
	blobs := newMockBlobStore()

	rec := analyze.NewReconciler(ms, blobs)
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
