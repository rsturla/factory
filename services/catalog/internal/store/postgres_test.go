package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/catalog/internal/model"
	"github.com/rsturla/factory/services/catalog/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://catalogdb:catalogdb@localhost:5432/catalogdb?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("postgres not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres not reachable: %v", err)
	}
	if err := store.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrations failed: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, "DELETE FROM platform_packages")
		pool.Exec(ctx, "DELETE FROM sboms")
		pool.Exec(ctx, "DELETE FROM packages")
		pool.Exec(ctx, "DELETE FROM platforms")
		pool.Exec(ctx, "DELETE FROM image_tags")
		pool.Exec(ctx, "DELETE FROM images")
		pool.Exec(ctx, "DELETE FROM discover_checkpoints")
		pool.Close()
	})
	return pool
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	pool := testPool(t)
	return store.NewPGStore(pool)
}

func TestPing(t *testing.T) {
	s := testStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if err := store.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func TestUpsertAndGetImage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{
		ID:     "sha256:abc123",
		Digest: "sha256:abc123",
	}
	if err := s.UpsertImage(ctx, img); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Also create a tag for this image.
	if err := s.UpsertTag(ctx, img.ID, model.Tag{
		Registry:   "quay.io",
		Repository: "hummingbird/core-runtime",
		Tag:        "latest",
	}); err != nil {
		t.Fatalf("upsert tag: %v", err)
	}

	got, err := s.GetImage(ctx, "sha256:abc123")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected image, got nil")
	}
	if got.Digest != "sha256:abc123" {
		t.Errorf("digest: got %q, want sha256:abc123", got.Digest)
	}
	if len(got.Tags) != 1 {
		t.Fatalf("tags count: got %d, want 1", len(got.Tags))
	}
	if got.Tags[0].Repository != "hummingbird/core-runtime" {
		t.Errorf("repository: got %q, want hummingbird/core-runtime", got.Tags[0].Repository)
	}
}

func TestGetImageNotFound(t *testing.T) {
	s := testStore(t)
	got, err := s.GetImage(context.Background(), "sha256:nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestGetImageByTag(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{
		ID:     "sha256:def456",
		Digest: "sha256:def456",
	}
	s.UpsertImage(ctx, img) //nolint:errcheck
	s.UpsertTag(ctx, img.ID, model.Tag{
		Registry:   "quay.io",
		Repository: "hummingbird/go",
		Tag:        "1.26",
	}) //nolint:errcheck

	got, err := s.GetImageByTag(ctx, "quay.io", "hummingbird/go", "1.26")
	if err != nil {
		t.Fatalf("get by tag: %v", err)
	}
	if got == nil {
		t.Fatal("expected image, got nil")
	}
	if got.ID != "sha256:def456" {
		t.Errorf("id: got %q, want sha256:def456", got.ID)
	}
}

func TestGetImageByDigest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{
		ID:     "sha256:digest789",
		Digest: "sha256:digest789",
	}
	s.UpsertImage(ctx, img) //nolint:errcheck

	got, err := s.GetImageByDigest(ctx, "sha256:digest789")
	if err != nil {
		t.Fatalf("get by digest: %v", err)
	}
	if got == nil {
		t.Fatal("expected image, got nil")
	}
	if got.ID != "sha256:digest789" {
		t.Errorf("id: got %q, want sha256:digest789", got.ID)
	}
}

func TestListImages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		img := model.Image{
			ID:     "sha256:list" + string(rune('0'+i)),
			Digest: "sha256:list" + string(rune('0'+i)),
		}
		s.UpsertImage(ctx, img) //nolint:errcheck
	}

	got, total, err := s.ListImages(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) < 3 {
		t.Errorf("count: got %d, want >= 3", len(got))
	}
	if total < 3 {
		t.Errorf("total: got %d, want >= 3", total)
	}
}

func TestUpsertAndGetPlatform(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:platimg", Digest: "sha256:platimg"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	p := model.Platform{
		ID:           "sha256:plat-amd64",
		ImageID:      "sha256:platimg",
		OS:           "linux",
		Architecture: "amd64",
	}
	if err := s.UpsertPlatform(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetPlatform(ctx, "sha256:plat-amd64")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected platform, got nil")
	}
	if got.Architecture != "amd64" {
		t.Errorf("arch: got %q, want amd64", got.Architecture)
	}
}

func TestListPlatformsByImage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:multiarch", Digest: "sha256:multiarch"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	for _, arch := range []string{"amd64", "arm64"} {
		p := model.Platform{
			ID:           "sha256:multi-" + arch,
			ImageID:      "sha256:multiarch",
			OS:           "linux",
			Architecture: arch,
		}
		s.UpsertPlatform(ctx, p) //nolint:errcheck
	}

	got, err := s.ListPlatformsByImage(ctx, "sha256:multiarch")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2", len(got))
	}
}

func TestPackageCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	pkg := model.Package{
		PURL:      "pkg:rpm/redhat/glibc@2.39-22.el10",
		Type:      "rpm",
		Name:      "glibc",
		Version:   "2.39-22.el10",
		Namespace: "redhat",
	}
	id, err := s.UpsertPackage(ctx, pkg)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	// Search by name.
	found, err := s.SearchPackages(ctx, "glibc", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) < 1 {
		t.Fatalf("search returned 0 results")
	}
	if found[0].Name != "glibc" {
		t.Errorf("name: got %q, want glibc", found[0].Name)
	}
}

func TestAssociateAndListPackages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:pkgimg", Digest: "sha256:pkgimg"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	p := model.Platform{ID: "sha256:pkgplat", ImageID: "sha256:pkgimg", OS: "linux", Architecture: "amd64"}
	s.UpsertPlatform(ctx, p) //nolint:errcheck

	var ids []string
	for _, name := range []string{"openssl", "zlib"} {
		pkg := model.Package{
			PURL:    "pkg:rpm/redhat/" + name + "@1.0",
			Type:    "rpm",
			Name:    name,
			Version: "1.0",
		}
		id, err := s.UpsertPackage(ctx, pkg)
		if err != nil {
			t.Fatalf("upsert %s: %v", name, err)
		}
		ids = append(ids, id)
	}

	if err := s.AssociatePackages(ctx, "sha256:pkgplat", ids); err != nil {
		t.Fatalf("associate: %v", err)
	}

	got, err := s.ListPackagesByPlatform(ctx, "sha256:pkgplat")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("count: got %d, want 2", len(got))
	}
}

func TestGetImagesByPackage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:bypkg", Digest: "sha256:bypkg"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	p := model.Platform{ID: "sha256:bypkgplat", ImageID: "sha256:bypkg", OS: "linux", Architecture: "amd64"}
	s.UpsertPlatform(ctx, p) //nolint:errcheck

	pkg := model.Package{PURL: "pkg:rpm/redhat/curl@8.0", Type: "rpm", Name: "curl", Version: "8.0"}
	id, _ := s.UpsertPackage(ctx, pkg) //nolint:errcheck
	s.AssociatePackages(ctx, "sha256:bypkgplat", []string{id}) //nolint:errcheck

	images, err := s.GetImagesByPackage(ctx, "pkg:rpm/redhat/curl@8.0")
	if err != nil {
		t.Fatalf("get images by package: %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("count: got %d, want 1", len(images))
	}
	if images[0].ID != "sha256:bypkg" {
		t.Errorf("id: got %q, want sha256:bypkg", images[0].ID)
	}
}

func TestSBOMCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:sbomimg", Digest: "sha256:sbomimg"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	p := model.Platform{ID: "sha256:sbomplat", ImageID: "sha256:sbomimg", OS: "linux", Architecture: "amd64"}
	s.UpsertPlatform(ctx, p) //nolint:errcheck

	sbom := model.SBOM{
		PlatformID:  "sha256:sbomplat",
		Source:      "syft",
		Format:      "spdx-json",
		ContentHash: "abc123",
		Raw:         []byte(`{"spdxVersion":"SPDX-2.3"}`),
	}
	if err := s.UpsertSBOM(ctx, sbom); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetSBOM(ctx, "sha256:sbomplat", "syft")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected sbom, got nil")
	}
	if got.ContentHash != "abc123" {
		t.Errorf("hash: got %q, want abc123", got.ContentHash)
	}
	if string(got.Raw) != `{"spdxVersion":"SPDX-2.3"}` {
		t.Errorf("raw: got %q", string(got.Raw))
	}
}

func TestSBOMNotFound(t *testing.T) {
	s := testStore(t)
	got, err := s.GetSBOM(context.Background(), "sha256:nonexistent", "syft")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestCheckpoints(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Not found returns empty string.
	val, err := s.GetCheckpoint(ctx, "test-source")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty, got %q", val)
	}

	// Create.
	if err := s.UpdateCheckpoint(ctx, "test-source", "checkpoint-1"); err != nil {
		t.Fatalf("update: %v", err)
	}

	val, _ = s.GetCheckpoint(ctx, "test-source")
	if val != "checkpoint-1" {
		t.Errorf("value: got %q, want checkpoint-1", val)
	}

	// Update.
	s.UpdateCheckpoint(ctx, "test-source", "checkpoint-2") //nolint:errcheck
	val, _ = s.GetCheckpoint(ctx, "test-source")
	if val != "checkpoint-2" {
		t.Errorf("value: got %q, want checkpoint-2", val)
	}
}

func TestImageUpdatePreservesID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	img := model.Image{ID: "sha256:upd", Digest: "sha256:upd"}
	s.UpsertImage(ctx, img) //nolint:errcheck

	img.Digest = "sha256:upd-v2"
	s.UpsertImage(ctx, img) //nolint:errcheck

	got, _ := s.GetImage(ctx, "sha256:upd")
	if got.Digest != "sha256:upd-v2" {
		t.Errorf("digest: got %q, want sha256:upd-v2", got.Digest)
	}
}
