package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rsturla/factory/services/catalog/internal/api"
	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	images    map[string]*model.Image
	platforms map[string][]model.Platform
	packages  map[string][]model.Package // keyed by platform ID
	sboms     map[string]*model.SBOM     // keyed by platformID|source
	allPkgs   []model.Package
}

func newMockStore() *mockStore {
	return &mockStore{
		images:    make(map[string]*model.Image),
		platforms: make(map[string][]model.Platform),
		packages:  make(map[string][]model.Package),
		sboms:     make(map[string]*model.SBOM),
	}
}

func (m *mockStore) UpsertImage(_ context.Context, img model.Image) error {
	m.images[img.ID] = &img
	return nil
}

func (m *mockStore) GetImage(_ context.Context, id string) (*model.Image, error) {
	img, ok := m.images[id]
	if !ok {
		return nil, nil
	}
	cp := *img
	cp.Platforms = m.platforms[id]
	return &cp, nil
}

func (m *mockStore) GetImageByDigest(_ context.Context, digest string) (*model.Image, error) {
	for _, img := range m.images {
		if img.Digest == digest {
			cp := *img
			cp.Platforms = m.platforms[img.ID]
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockStore) UpsertTag(_ context.Context, _ string, _ model.Tag) error { return nil }
func (m *mockStore) ListTagsByImage(_ context.Context, _ string) ([]model.Tag, error) {
	return nil, nil
}
func (m *mockStore) GetImageByTag(_ context.Context, _, _, _ string) (*model.Image, error) {
	return nil, nil
}

func (m *mockStore) ListImages(_ context.Context, limit, offset int) ([]model.Image, int, error) {
	var out []model.Image
	for _, img := range m.images {
		out = append(out, *img)
	}
	total := len(out)
	if offset >= len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (m *mockStore) UpsertPlatform(_ context.Context, p model.Platform) error { return nil }
func (m *mockStore) GetPlatform(_ context.Context, id string) (*model.Platform, error) {
	return nil, nil
}
func (m *mockStore) ListPlatformsByImage(_ context.Context, imageID string) ([]model.Platform, error) {
	return m.platforms[imageID], nil
}

func (m *mockStore) UpsertPackage(_ context.Context, _ model.Package) (string, error) {
	return "", nil
}
func (m *mockStore) AssociatePackages(_ context.Context, _ string, _ []string) error { return nil }
func (m *mockStore) ListPackagesByPlatform(_ context.Context, platformID string) ([]model.Package, error) {
	return m.packages[platformID], nil
}
func (m *mockStore) SearchPackages(_ context.Context, name string, limit int) ([]model.Package, error) {
	var out []model.Package
	for _, pkg := range m.allPkgs {
		if strings.Contains(strings.ToLower(pkg.Name), strings.ToLower(name)) {
			out = append(out, pkg)
		}
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}
func (m *mockStore) GetImagesByPackage(_ context.Context, purl string) ([]model.Image, error) {
	var out []model.Image
	for _, img := range m.images {
		out = append(out, *img)
	}
	return out, nil
}

func (m *mockStore) GetImagesByPackageName(_ context.Context, _, _ string, _ int) ([]model.Image, error) {
	return nil, nil
}
func (m *mockStore) DiffPackages(_ context.Context, _, _ string) ([]model.Package, []model.Package, error) {
	return nil, nil, nil
}
func (m *mockStore) UpsertSBOM(_ context.Context, _ model.SBOM) error { return nil }
func (m *mockStore) GetSBOM(_ context.Context, platformID, source string) (*model.SBOM, error) {
	sbom, ok := m.sboms[platformID+"|"+source]
	if !ok {
		return nil, nil
	}
	return sbom, nil
}

func (m *mockStore) GetCheckpoint(_ context.Context, _ string) (string, error) { return "", nil }
func (m *mockStore) UpdateCheckpoint(_ context.Context, _, _ string) error      { return nil }
func (m *mockStore) Ping(_ context.Context) error                               { return nil }
func (m *mockStore) Close()                                                      {}

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

func seedImage(ms *mockStore, id, repo, tag string) {
	ms.images[id] = &model.Image{
		ID:     id,
		Digest: id,
		Tags: []model.Tag{
			{Registry: "quay.io", Repository: repo, Tag: tag},
		},
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/images
// ---------------------------------------------------------------------------

func TestListImages_Empty(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")
}

func TestListImages_WithData(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:aaa", "quay.io/hummingbird/core-runtime", "latest")
	seedImage(ms, "sha256:bbb", "quay.io/hummingbird/go", "1.26")

	rec := doRequest(t, mux, "GET", "/api/v1/images", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestListImages_Pagination(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	for i := 0; i < 5; i++ {
		seedImage(ms, "sha256:page"+string(rune('0'+i)), "quay.io/test/img"+string(rune('0'+i)), "v1")
	}

	rec := doRequest(t, mux, "GET", "/api/v1/images?limit=2", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/images/{id}
// ---------------------------------------------------------------------------

func TestGetImage_Found(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)
	seedImage(ms, "sha256:found", "quay.io/test/found", "v1")

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:found", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["id"] != "sha256:found" {
		t.Fatalf("id = %v, want sha256:found", resp["id"])
	}
}

func TestGetImage_NotFound(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:nope", "")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /api/v1/images/{id}/packages
// ---------------------------------------------------------------------------

func TestGetImagePackages(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:pkgimg", "quay.io/test/pkg", "v1")
	ms.platforms["sha256:pkgimg"] = []model.Platform{
		{ID: "sha256:plat-amd64", ImageID: "sha256:pkgimg", OS: "linux", Architecture: "amd64"},
	}
	ms.packages["sha256:plat-amd64"] = []model.Package{
		{ID: "p1", PURL: "pkg:rpm/redhat/glibc@2.39", Type: "rpm", Name: "glibc", Version: "2.39"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:pkgimg/packages", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	platforms := resp["platforms"].([]any)
	if len(platforms) != 1 {
		t.Fatalf("platforms count = %d, want 1", len(platforms))
	}
}

func TestGetImagePackages_ArchFilter(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:archimg", "quay.io/test/arch", "v1")
	ms.platforms["sha256:archimg"] = []model.Platform{
		{ID: "sha256:arch-amd64", ImageID: "sha256:archimg", OS: "linux", Architecture: "amd64"},
		{ID: "sha256:arch-arm64", ImageID: "sha256:archimg", OS: "linux", Architecture: "arm64"},
	}
	ms.packages["sha256:arch-amd64"] = []model.Package{
		{ID: "p1", Name: "glibc"},
	}
	ms.packages["sha256:arch-arm64"] = []model.Package{
		{ID: "p2", Name: "glibc"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:archimg/packages?arch=arm64", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	platforms := resp["platforms"].([]any)
	if len(platforms) != 1 {
		t.Fatalf("platforms count = %d, want 1 (filtered to arm64)", len(platforms))
	}
	p := platforms[0].(map[string]any)
	if p["architecture"] != "arm64" {
		t.Fatalf("architecture = %v, want arm64", p["architecture"])
	}
}

func TestGetImagePackages_ImageNotFound(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:nope/packages", "")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /api/v1/images/{id}/sbom
// ---------------------------------------------------------------------------

func TestGetImageSBOM(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:sbomimg", "quay.io/test/sbom", "v1")
	ms.platforms["sha256:sbomimg"] = []model.Platform{
		{ID: "sha256:sbom-amd64", ImageID: "sha256:sbomimg", OS: "linux", Architecture: "amd64"},
	}
	ms.sboms["sha256:sbom-amd64|syft"] = &model.SBOM{
		ID:          "s1",
		PlatformID:  "sha256:sbom-amd64",
		Source:      "syft",
		Format:      "spdx-json",
		ContentHash: "hash",
		Raw:         []byte(`{"spdxVersion":"SPDX-2.3"}`),
	}

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:sbomimg/sbom?source=syft&arch=amd64", "")
	assertStatus(t, rec, http.StatusOK)

	body := rec.Body.String()
	if !strings.Contains(body, "SPDX-2.3") {
		t.Fatalf("body does not contain SPDX-2.3: %s", body)
	}
}

func TestGetImageSBOM_NotFound(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:nosbom", "quay.io/test/nosbom", "v1")
	ms.platforms["sha256:nosbom"] = []model.Platform{
		{ID: "sha256:nosbom-amd64", ImageID: "sha256:nosbom", OS: "linux", Architecture: "amd64"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/images/sha256:nosbom/sbom?arch=amd64", "")
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /api/v1/packages
// ---------------------------------------------------------------------------

func TestSearchPackages(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	ms.allPkgs = []model.Package{
		{ID: "p1", PURL: "pkg:rpm/redhat/glibc@2.39", Type: "rpm", Name: "glibc", Version: "2.39"},
		{ID: "p2", PURL: "pkg:rpm/redhat/openssl@3.0", Type: "rpm", Name: "openssl", Version: "3.0"},
	}

	rec := doRequest(t, mux, "GET", "/api/v1/packages?name=glibc", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestSearchPackages_MissingName(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/packages", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// GET /api/v1/packages/{purl}/images
// ---------------------------------------------------------------------------

func TestGetPackageImages(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	seedImage(ms, "sha256:pkgimages", "quay.io/test/img", "v1")

	rec := doRequest(t, mux, "GET", "/api/v1/packages/images?purl=pkg:rpm/redhat/glibc@2.39", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	count := int(resp["count"].(float64))
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestGetPackageImages_MissingPurl(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/packages/images", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// intParam edge cases
// ---------------------------------------------------------------------------

func TestIntParam_InvalidFallsToDefault(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images?limit=notanumber", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestIntParam_NegativeFallsToDefault(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images?limit=-5", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestIntParam_LimitCappedAt1000(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/images?limit=5000", "")
	assertStatus(t, rec, http.StatusOK)
}

func TestSearchImagesByPackage_NameRequired(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/search/images-by-package", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestSearchImagesByPackage_NameOnly(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/search/images-by-package?name=coreutils", "")
	assertStatus(t, rec, http.StatusOK)
	assertContentType(t, rec, "application/json")

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["package_name"] != "coreutils" {
		t.Errorf("package_name: got %q, want %q", resp["package_name"], "coreutils")
	}
	if _, ok := resp["package_version"]; ok {
		t.Error("package_version should not be present when version not specified")
	}
}

func TestSearchImagesByPackage_WithVersion(t *testing.T) {
	ms := newMockStore()
	mux := setupServer(t, ms)

	rec := doRequest(t, mux, "GET", "/api/v1/search/images-by-package?name=coreutils&version=9.1", "")
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["package_name"] != "coreutils" {
		t.Errorf("package_name: got %q, want %q", resp["package_name"], "coreutils")
	}
	if resp["package_version"] != "9.1" {
		t.Errorf("package_version: got %q, want %q", resp["package_version"], "9.1")
	}
}
