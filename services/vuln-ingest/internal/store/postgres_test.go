package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://vulndb:vulndb@localhost:5432/vulndb?sslmode=disable"
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
		// Clean test data.
		pool.Exec(ctx, "DELETE FROM affected_packages")
		pool.Exec(ctx, "DELETE FROM source_records")
		pool.Exec(ctx, "DELETE FROM epss_scores")
		pool.Exec(ctx, "DELETE FROM kev_entries")
		pool.Exec(ctx, "DELETE FROM vulnerabilities")
		pool.Exec(ctx, "DELETE FROM source_checkpoints")
		pool.Close()
	})
	return pool
}

func testStore(t *testing.T) store.Store {
	t.Helper()
	pool := testPool(t)
	return store.NewPGStore(pool)
}

func timePtr(t time.Time) *time.Time { return &t }

var testTime = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

func TestPing(t *testing.T) {
	s := testStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestUpsertAndGetVulnerability(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v := &model.Vulnerability{
		ID:        "CVE-2024-0001",
		Aliases:   []string{"GHSA-xxxx-yyyy-zzzz"},
		Summary:   "Test vulnerability",
		Details:   "Detailed description of the test vulnerability",
		Severity:  []model.Severity{{Type: "CVSS_V3_1", Score: "7.5", Vector: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"}},
		Published: timePtr(testTime),
		Modified:  timePtr(testTime.Add(24 * time.Hour)),
		References: []model.Reference{
			{Type: "ADVISORY", URL: "https://example.com/advisory/1"},
		},
		AffectedPackages: []model.AffectedPackage{
			{
				Ecosystem:   "npm",
				PackageName: "express",
				Purl:        "pkg:npm/express",
				VersionRanges: []model.VersionRange{
					{Introduced: "4.0.0", Fixed: "4.18.2"},
				},
				QualityFlags: []string{},
			},
			{
				Ecosystem:   "npm",
				PackageName: "express",
				Purl:        "pkg:npm/express",
				VersionRanges: []model.VersionRange{
					{Introduced: "5.0.0-alpha.1", Fixed: "5.0.0-beta.3"},
				},
			},
		},
	}

	if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetVulnerability(ctx, "CVE-2024-0001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected vulnerability, got nil")
	}
	if got.ID != "CVE-2024-0001" {
		t.Errorf("id: got %q, want %q", got.ID, "CVE-2024-0001")
	}
	if got.Summary != "Test vulnerability" {
		t.Errorf("summary: got %q, want %q", got.Summary, "Test vulnerability")
	}
	if len(got.AffectedPackages) != 2 {
		t.Errorf("affected packages: got %d, want 2", len(got.AffectedPackages))
	}
	if len(got.Severity) != 1 {
		t.Errorf("severity: got %d, want 1", len(got.Severity))
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "GHSA-xxxx-yyyy-zzzz" {
		t.Errorf("aliases: got %v, want [GHSA-xxxx-yyyy-zzzz]", got.Aliases)
	}
}

func TestUpsertVulnerabilityUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v := &model.Vulnerability{
		ID:      "CVE-2024-0002",
		Summary: "Original",
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "PyPI", PackageName: "flask"},
		},
	}
	if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	v.Summary = "Updated"
	v.AffectedPackages = []model.AffectedPackage{
		{Ecosystem: "PyPI", PackageName: "flask", VersionRanges: []model.VersionRange{{Fixed: "2.0.0"}}},
		{Ecosystem: "PyPI", PackageName: "werkzeug"},
	}
	if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, _ := s.GetVulnerability(ctx, "CVE-2024-0002")
	if got.Summary != "Updated" {
		t.Errorf("summary: got %q, want %q", got.Summary, "Updated")
	}
	if len(got.AffectedPackages) != 2 {
		t.Errorf("affected: got %d, want 2", len(got.AffectedPackages))
	}
}

func TestGetVulnerabilityNotFound(t *testing.T) {
	s := testStore(t)
	got, err := s.GetVulnerability(context.Background(), "CVE-9999-0000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestBatchGetVulnerabilities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := range 5 {
		v := &model.Vulnerability{
			ID:       "CVE-2024-1" + string(rune('0'+i)),
			Summary:  "Vuln " + string(rune('0'+i)),
			Modified: timePtr(testTime.Add(time.Duration(i) * time.Hour)),
		}
		s.UpsertVulnerability(ctx, v, "test") //nolint:errcheck
	}

	// Request in specific order, including non-existent.
	ids := []string{"CVE-2024-13", "CVE-2024-11", "DOES-NOT-EXIST", "CVE-2024-10"}
	got, err := s.BatchGetVulnerabilities(ctx, ids)
	if err != nil {
		t.Fatalf("batch get: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("count: got %d, want 3", len(got))
	}
	// Order preserved.
	if got[0].ID != "CVE-2024-13" {
		t.Errorf("first: got %q, want CVE-2024-13", got[0].ID)
	}
	if got[1].ID != "CVE-2024-11" {
		t.Errorf("second: got %q, want CVE-2024-11", got[1].ID)
	}
	if got[2].ID != "CVE-2024-10" {
		t.Errorf("third: got %q, want CVE-2024-10", got[2].ID)
	}
}

func TestBatchGetEmpty(t *testing.T) {
	s := testStore(t)
	got, err := s.BatchGetVulnerabilities(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestListVulnerabilities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := range 5 {
		v := &model.Vulnerability{
			ID:       "CVE-2024-200" + string(rune('0'+i)),
			Modified: timePtr(testTime.Add(time.Duration(i) * time.Hour)),
		}
		s.UpsertVulnerability(ctx, v, "test") //nolint:errcheck
	}

	t.Run("default", func(t *testing.T) {
		got, err := s.ListVulnerabilities(ctx, store.ListOpts{})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) < 5 {
			t.Errorf("count: got %d, want >= 5", len(got))
		}
	})

	t.Run("limit", func(t *testing.T) {
		got, err := s.ListVulnerabilities(ctx, store.ListOpts{Limit: 2})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("count: got %d, want 2", len(got))
		}
	})

	t.Run("modified_since", func(t *testing.T) {
		since := testTime.Add(2 * time.Hour)
		got, err := s.ListVulnerabilities(ctx, store.ListOpts{ModifiedSince: &since})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, v := range got {
			if v.Modified != nil && v.Modified.Before(since) {
				t.Errorf("vuln %s modified %v is before filter %v", v.ID, v.Modified, since)
			}
		}
	})
}

func TestListAffectedByPackage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v := &model.Vulnerability{
		ID: "CVE-2024-3001",
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "Go", PackageName: "golang.org/x/net"},
		},
	}
	s.UpsertVulnerability(ctx, v, "test") //nolint:errcheck

	got, err := s.ListAffectedByPackage(ctx, "Go", "golang.org/x/net", store.ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("count: got %d, want 1", len(got))
	}
	if got[0].ID != "CVE-2024-3001" {
		t.Errorf("id: got %q, want CVE-2024-3001", got[0].ID)
	}

	// Non-existent package.
	got, err = s.ListAffectedByPackage(ctx, "Go", "nonexistent", store.ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("count: got %d, want 0", len(got))
	}
}

func TestSourceRecords(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v := &model.Vulnerability{ID: "CVE-2024-4001"}
	s.UpsertVulnerability(ctx, v, "test") //nolint:errcheck

	rec := &model.SourceRecord{
		VulnID:  "CVE-2024-4001",
		Source:  "nvd",
		RawHash: "abc123",
	}
	if err := s.UpsertSourceRecord(ctx, rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetSourceRecord(ctx, "CVE-2024-4001", "nvd")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}
	if got.RawHash != "abc123" {
		t.Errorf("hash: got %q, want abc123", got.RawHash)
	}

	// Update hash.
	rec.RawHash = "def456"
	s.UpsertSourceRecord(ctx, rec) //nolint:errcheck
	got, _ = s.GetSourceRecord(ctx, "CVE-2024-4001", "nvd")
	if got.RawHash != "def456" {
		t.Errorf("updated hash: got %q, want def456", got.RawHash)
	}

	// Not found.
	got, _ = s.GetSourceRecord(ctx, "CVE-2024-4001", "ghsa")
	if got != nil {
		t.Errorf("expected nil for missing source, got %v", got)
	}
}

func TestCheckpoints(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Not found.
	cp, err := s.GetCheckpoint(ctx, "test-source")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if cp != nil {
		t.Errorf("expected nil, got %v", cp)
	}

	// Create.
	if err := s.UpdateCheckpoint(ctx, "test-source", "commit-abc", 100); err != nil {
		t.Fatalf("update: %v", err)
	}

	cp, _ = s.GetCheckpoint(ctx, "test-source")
	if cp == nil {
		t.Fatal("expected checkpoint")
	}
	if cp.CheckpointValue != "commit-abc" {
		t.Errorf("value: got %q, want commit-abc", cp.CheckpointValue)
	}
	if cp.ItemsSynced != 100 {
		t.Errorf("items: got %d, want 100", cp.ItemsSynced)
	}

	// Increment.
	s.UpdateCheckpoint(ctx, "test-source", "commit-def", 50) //nolint:errcheck
	cp, _ = s.GetCheckpoint(ctx, "test-source")
	if cp.CheckpointValue != "commit-def" {
		t.Errorf("value: got %q, want commit-def", cp.CheckpointValue)
	}
	if cp.ItemsSynced != 150 {
		t.Errorf("items: got %d, want 150", cp.ItemsSynced)
	}

	// Error.
	s.SetCheckpointError(ctx, "test-source", "fetch failed") //nolint:errcheck
	cp, _ = s.GetCheckpoint(ctx, "test-source")
	if cp.ErrorMessage != "fetch failed" {
		t.Errorf("error: got %q, want 'fetch failed'", cp.ErrorMessage)
	}

	// Error cleared on update.
	s.UpdateCheckpoint(ctx, "test-source", "commit-ghi", 10) //nolint:errcheck
	cp, _ = s.GetCheckpoint(ctx, "test-source")
	if cp.ErrorMessage != "" {
		t.Errorf("error should be cleared, got %q", cp.ErrorMessage)
	}
}

func TestSetCheckpointErrorNewSource(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	if err := s.SetCheckpointError(ctx, "brand-new-source", "connection refused"); err != nil {
		t.Fatalf("set error on new source: %v", err)
	}

	cp, _ := s.GetCheckpoint(ctx, "brand-new-source")
	if cp == nil {
		t.Fatal("expected checkpoint created for error")
	}
	if cp.ErrorMessage != "connection refused" {
		t.Errorf("error: got %q, want 'connection refused'", cp.ErrorMessage)
	}
}

func TestListCheckpoints(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	s.UpdateCheckpoint(ctx, "source-b", "v2", 10) //nolint:errcheck
	s.UpdateCheckpoint(ctx, "source-a", "v1", 5)  //nolint:errcheck

	list, err := s.ListCheckpoints(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("count: got %d, want >= 2", len(list))
	}

	// Ordered by source name.
	foundA, foundB := false, false
	for i, cp := range list {
		if cp.Source == "source-a" {
			foundA = true
		}
		if cp.Source == "source-b" {
			foundB = true
			if i > 0 && list[i-1].Source > cp.Source {
				t.Error("checkpoints not ordered by source")
			}
		}
	}
	if !foundA || !foundB {
		t.Error("missing expected checkpoints")
	}
}

func TestKEVEntries(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	dateAdded := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	entries := []model.KEVEntry{
		{
			CVEID:         "CVE-2024-5001",
			VendorProject: "Apache",
			Product:       "httpd",
			DateAdded:     &dateAdded,
		},
		{
			CVEID:         "CVE-2024-5002",
			VendorProject: "Linux",
			Product:       "kernel",
		},
	}

	if err := s.UpsertKEVEntries(ctx, entries); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetKEVEntry(ctx, "CVE-2024-5001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected entry")
	}
	if got.VendorProject != "Apache" {
		t.Errorf("vendor: got %q, want Apache", got.VendorProject)
	}

	// Not found.
	got, _ = s.GetKEVEntry(ctx, "CVE-9999-0000")
	if got != nil {
		t.Errorf("expected nil, got %v", got)
	}

	// Empty input.
	if err := s.UpsertKEVEntries(ctx, nil); err != nil {
		t.Fatalf("empty upsert should not error: %v", err)
	}
}

func TestEPSSScores(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	scores := []model.EPSSScore{
		{CVEID: "CVE-2024-6001", Score: 0.95, Percentile: 0.99, ModelVersion: "v2024.01.01", ScoreDate: timePtr(testTime)},
		{CVEID: "CVE-2024-6002", Score: 0.01, Percentile: 0.10, ScoreDate: timePtr(testTime)},
	}

	if err := s.UpsertEPSSScores(ctx, scores); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.GetEPSSScore(ctx, "CVE-2024-6001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected score")
	}
	if got.Score != 0.95 {
		t.Errorf("score: got %f, want 0.95", got.Score)
	}

	// Score map for diffing.
	m, err := s.GetAllEPSSScoreMap(ctx)
	if err != nil {
		t.Fatalf("score map: %v", err)
	}
	if m["CVE-2024-6001"] != 0.95 {
		t.Errorf("map score: got %f, want 0.95", m["CVE-2024-6001"])
	}

	// Update score.
	updated := []model.EPSSScore{{CVEID: "CVE-2024-6001", Score: 0.80, Percentile: 0.90, ScoreDate: timePtr(testTime.Add(24 * time.Hour))}}
	s.UpsertEPSSScores(ctx, updated) //nolint:errcheck
	got, _ = s.GetEPSSScore(ctx, "CVE-2024-6001")
	if got.Score != 0.80 {
		t.Errorf("updated score: got %f, want 0.80", got.Score)
	}

	// Empty input.
	if err := s.UpsertEPSSScores(ctx, nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
}

func TestGetAllKEVIDs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	entries := []model.KEVEntry{
		{CVEID: "CVE-2024-7001"},
		{CVEID: "CVE-2024-7002"},
	}
	s.UpsertKEVEntries(ctx, entries) //nolint:errcheck

	m, err := s.GetAllKEVIDs(ctx)
	if err != nil {
		t.Fatalf("get all kev ids: %v", err)
	}
	if len(m) < 2 {
		t.Errorf("count: got %d, want >= 2", len(m))
	}
	if _, ok := m["CVE-2024-7001"]; !ok {
		t.Error("missing CVE-2024-7001")
	}
}

func TestMigrationIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Running migrations twice should not error.
	if err := store.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func TestUpsertVulnerabilityMultiSource(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	// Upsert from source "nvd" with 2 affected packages.
	v := &model.Vulnerability{
		ID:      "CVE-2024-9001",
		Summary: "multi-source test",
		AffectedPackages: []model.AffectedPackage{
			{Source: "nvd", Ecosystem: "npm", PackageName: "express"},
			{Source: "nvd", Ecosystem: "npm", PackageName: "koa"},
		},
	}
	if err := s.UpsertVulnerability(ctx, v, "nvd"); err != nil {
		t.Fatalf("upsert from nvd: %v", err)
	}

	// Upsert from source "ghsa" with 1 different affected package.
	v2 := &model.Vulnerability{
		ID:      "CVE-2024-9001",
		Summary: "multi-source test updated",
		AffectedPackages: []model.AffectedPackage{
			{Source: "ghsa", Ecosystem: "npm", PackageName: "fastify"},
		},
	}
	if err := s.UpsertVulnerability(ctx, v2, "ghsa"); err != nil {
		t.Fatalf("upsert from ghsa: %v", err)
	}

	got, err := s.GetVulnerability(ctx, "CVE-2024-9001")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected vulnerability, got nil")
	}
	if len(got.AffectedPackages) != 3 {
		t.Errorf("affected packages: got %d, want 3 (2 from nvd + 1 from ghsa)", len(got.AffectedPackages))
	}
}

func TestListAffectedByPurl(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	v := &model.Vulnerability{
		ID: "CVE-2024-PURL1",
		AffectedPackages: []model.AffectedPackage{
			{Ecosystem: "npm", PackageName: "express", Purl: "pkg:npm/express@4.18.0"},
		},
	}
	if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.ListAffectedByPurl(ctx, "pkg:npm/express@4.18.0", store.ListOpts{})
	if err != nil {
		t.Fatalf("list by purl: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("count: got %d, want 1", len(got))
	}
	if got[0].ID != "CVE-2024-PURL1" {
		t.Errorf("id: got %q, want CVE-2024-PURL1", got[0].ID)
	}

	// Non-existent purl.
	got, err = s.ListAffectedByPurl(ctx, "pkg:npm/nonexistent@0.0.0", store.ListOpts{})
	if err != nil {
		t.Fatalf("list by purl: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("count: got %d, want 0", len(got))
	}
}

func TestCountVulnerabilities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		v := &model.Vulnerability{
			ID:       fmt.Sprintf("CVE-2024-CNT%d", i),
			Modified: timePtr(testTime.Add(time.Duration(i) * time.Hour)),
		}
		if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	count, err := s.CountVulnerabilities(ctx, store.ListOpts{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count < 5 {
		t.Errorf("count: got %d, want >= 5", count)
	}
}

func TestCountAffectedByPackage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		v := &model.Vulnerability{
			ID: fmt.Sprintf("CVE-2024-CAPKG%d", i),
			AffectedPackages: []model.AffectedPackage{
				{Ecosystem: "PyPI", PackageName: "requests"},
			},
		}
		if err := s.UpsertVulnerability(ctx, v, "test"); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	count, err := s.CountAffectedByPackage(ctx, "PyPI", "requests")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestListVulnerabilitiesUpdatedSince(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	old := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	v1 := &model.Vulnerability{ID: "CVE-2024-US-OLD", Modified: &old}
	v2 := &model.Vulnerability{ID: "CVE-2024-US-NEW", Modified: &recent}

	if err := s.UpsertVulnerability(ctx, v1, "test"); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if err := s.UpsertVulnerability(ctx, v2, "test"); err != nil {
		t.Fatalf("upsert new: %v", err)
	}

	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := s.ListVulnerabilities(ctx, store.ListOpts{UpdatedSince: &since})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// At minimum we should get the recent one. The old one might show up
	// if updated_since filters on the DB updated_at column rather than
	// the modified field. Check that we get at least 1 result.
	if len(got) < 1 {
		t.Fatalf("expected at least 1 vuln with updated_since filter, got %d", len(got))
	}

	// Verify the recent one is included.
	found := false
	for _, v := range got {
		if v.ID == "CVE-2024-US-NEW" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected CVE-2024-US-NEW in results")
	}
}
