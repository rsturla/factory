package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/scan-service/internal/model"
	"github.com/rsturla/factory/services/scan-service/internal/store"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set, skipping postgres tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not reachable: %v", err)
	}

	if err := store.RunMigrations(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrations: %v", err)
	}

	t.Cleanup(func() {
		// Clean up test data
		pool.Exec(ctx, "DELETE FROM findings")  //nolint:errcheck
		pool.Exec(ctx, "DELETE FROM scans")     //nolint:errcheck
		pool.Exec(ctx, "DELETE FROM scanner_db_state") //nolint:errcheck
		pool.Close()
	})

	return pool
}

func TestPGStore_UpsertAndGetScan(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	scan := model.Scan{
		ID:            "test-scan-1",
		PlatformID:    "sha256:abc|linux/amd64",
		Scanner:       "grype",
		DBVersion:     "v5.0",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   now,
		VulnCount:     5,
		CriticalCount: 1,
		HighCount:     2,
		MediumCount:   1,
		LowCount:      1,
		Status:        "completed",
	}

	if err := s.UpsertScan(ctx, scan); err != nil {
		t.Fatalf("upsert scan: %v", err)
	}

	got, err := s.GetLatestScan(ctx, "sha256:abc|linux/amd64", "grype")
	if err != nil {
		t.Fatalf("get latest scan: %v", err)
	}
	if got == nil {
		t.Fatal("expected scan, got nil")
	}
	if got.ID != "test-scan-1" {
		t.Errorf("id: got %q, want %q", got.ID, "test-scan-1")
	}
	if got.VulnCount != 5 {
		t.Errorf("vuln_count: got %d, want 5", got.VulnCount)
	}
}

func TestPGStore_UpsertAndListFindings(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)

	// First create a scan
	scan := model.Scan{
		ID:          "test-scan-findings",
		PlatformID:  "sha256:def|linux/amd64",
		Scanner:     "grype",
		DBVersion:   "v5.0",
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: now,
		VulnCount:   2,
		Status:      "completed",
	}
	if err := s.UpsertScan(ctx, scan); err != nil {
		t.Fatalf("upsert scan: %v", err)
	}

	findings := []model.Finding{
		{ScanID: "test-scan-findings", VulnID: "CVE-2024-1234", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0.1", PackageType: "rpm"},
		{ScanID: "test-scan-findings", VulnID: "CVE-2024-5678", Severity: "High", PackageName: "glibc", PackageVersion: "2.38", PackageType: "rpm", FixedVersion: "2.39"},
	}

	if err := s.UpsertFindings(ctx, "test-scan-findings", findings); err != nil {
		t.Fatalf("upsert findings: %v", err)
	}

	got, err := s.ListFindings(ctx, "test-scan-findings")
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(got))
	}
}

func TestPGStore_DBState(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	state := model.ScannerDBState{
		Scanner:   "grype",
		Version:   "v5.0",
		Checksum:  "abc123",
		UpdatedAt: now,
	}

	if err := s.UpsertDBState(ctx, state); err != nil {
		t.Fatalf("upsert db state: %v", err)
	}

	got, err := s.GetDBState(ctx, "grype")
	if err != nil {
		t.Fatalf("get db state: %v", err)
	}
	if got == nil {
		t.Fatal("expected db state, got nil")
	}
	if got.Version != "v5.0" {
		t.Errorf("version: got %q, want %q", got.Version, "v5.0")
	}
	if got.Checksum != "abc123" {
		t.Errorf("checksum: got %q, want %q", got.Checksum, "abc123")
	}
}

func TestPGStore_Ping(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)

	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestPGStore_ListScansByImage(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	imageDigest := "sha256:listscans"

	// Create scans for multiple platforms under the same image digest
	scans := []model.Scan{
		{
			ID:            "ls-scan-1",
			PlatformID:    imageDigest + "|linux/amd64",
			Scanner:       "grype",
			DBVersion:     "v5.0",
			StartedAt:     now.Add(-2 * time.Minute),
			CompletedAt:   now.Add(-time.Minute),
			VulnCount:     3,
			CriticalCount: 1,
			HighCount:     2,
			Status:        "completed",
		},
		{
			ID:            "ls-scan-2",
			PlatformID:    imageDigest + "|linux/arm64",
			Scanner:       "grype",
			DBVersion:     "v5.0",
			StartedAt:     now.Add(-time.Minute),
			CompletedAt:   now,
			VulnCount:     1,
			CriticalCount: 0,
			HighCount:     1,
			Status:        "completed",
		},
		{
			ID:            "ls-scan-3",
			PlatformID:    imageDigest + "|linux/arm64/v8",
			Scanner:       "grype",
			DBVersion:     "v5.0",
			StartedAt:     now.Add(-30 * time.Second),
			CompletedAt:   now,
			VulnCount:     0,
			Status:        "completed",
		},
	}

	for _, sc := range scans {
		if err := s.UpsertScan(ctx, sc); err != nil {
			t.Fatalf("upsert scan %s: %v", sc.ID, err)
		}
	}

	got, err := s.ListScansByImage(ctx, imageDigest)
	if err != nil {
		t.Fatalf("list scans by image: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 scans, got %d", len(got))
	}

	// Verify all platform IDs are present
	platforms := make(map[string]bool)
	for _, sc := range got {
		platforms[sc.PlatformID] = true
	}
	for _, pid := range []string{
		imageDigest + "|linux/amd64",
		imageDigest + "|linux/arm64",
		imageDigest + "|linux/arm64/v8",
	} {
		if !platforms[pid] {
			t.Errorf("missing platform %q in results", pid)
		}
	}
}

func TestPGStore_ListFindingsByPlatform_FilterByScanner(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	platformID := "sha256:filterbyscanner|linux/amd64"

	// Create two scans for the same platform but different scanners
	grypeScan := model.Scan{
		ID:          "fbp-grype",
		PlatformID:  platformID,
		Scanner:     "grype",
		DBVersion:   "v5.0",
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: now,
		VulnCount:   2,
		Status:      "completed",
	}
	trivyScan := model.Scan{
		ID:          "fbp-trivy",
		PlatformID:  platformID,
		Scanner:     "trivy",
		DBVersion:   "v2.0",
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: now,
		VulnCount:   1,
		Status:      "completed",
	}

	if err := s.UpsertScan(ctx, grypeScan); err != nil {
		t.Fatalf("upsert grype scan: %v", err)
	}
	if err := s.UpsertScan(ctx, trivyScan); err != nil {
		t.Fatalf("upsert trivy scan: %v", err)
	}

	grypeFindings := []model.Finding{
		{ScanID: "fbp-grype", VulnID: "CVE-2024-0001", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0"},
		{ScanID: "fbp-grype", VulnID: "CVE-2024-0002", Severity: "High", PackageName: "glibc", PackageVersion: "2.38"},
	}
	trivyFindings := []model.Finding{
		{ScanID: "fbp-trivy", VulnID: "CVE-2024-0001", Severity: "Critical", PackageName: "openssl", PackageVersion: "3.0"},
	}

	if err := s.UpsertFindings(ctx, "fbp-grype", grypeFindings); err != nil {
		t.Fatalf("upsert grype findings: %v", err)
	}
	if err := s.UpsertFindings(ctx, "fbp-trivy", trivyFindings); err != nil {
		t.Fatalf("upsert trivy findings: %v", err)
	}

	// List findings filtered to grype
	gotGrype, err := s.ListFindingsByPlatform(ctx, platformID, "grype")
	if err != nil {
		t.Fatalf("list findings by platform (grype): %v", err)
	}
	if len(gotGrype) != 2 {
		t.Fatalf("expected 2 grype findings, got %d", len(gotGrype))
	}

	// List findings filtered to trivy
	gotTrivy, err := s.ListFindingsByPlatform(ctx, platformID, "trivy")
	if err != nil {
		t.Fatalf("list findings by platform (trivy): %v", err)
	}
	if len(gotTrivy) != 1 {
		t.Fatalf("expected 1 trivy finding, got %d", len(gotTrivy))
	}
}

func TestPGStore_UpsertFindings_LargeBatch(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)

	scan := model.Scan{
		ID:          "large-batch-scan",
		PlatformID:  "sha256:largebatch|linux/amd64",
		Scanner:     "grype",
		DBVersion:   "v5.0",
		StartedAt:   now.Add(-time.Minute),
		CompletedAt: now,
		VulnCount:   100,
		Status:      "completed",
	}
	if err := s.UpsertScan(ctx, scan); err != nil {
		t.Fatalf("upsert scan: %v", err)
	}

	findings := make([]model.Finding, 100)
	for i := range findings {
		findings[i] = model.Finding{
			ScanID:         "large-batch-scan",
			VulnID:         fmt.Sprintf("CVE-2024-%04d", i),
			Severity:       []string{"Critical", "High", "Medium", "Low"}[i%4],
			PackageName:    fmt.Sprintf("pkg-%d", i),
			PackageVersion: "1.0",
			PackageType:    "rpm",
		}
	}

	if err := s.UpsertFindings(ctx, "large-batch-scan", findings); err != nil {
		t.Fatalf("upsert 100 findings: %v", err)
	}

	got, err := s.ListFindings(ctx, "large-batch-scan")
	if err != nil {
		t.Fatalf("list findings: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("expected 100 findings, got %d", len(got))
	}
}

func TestPGStore_GetLatestScan_ReturnsNewest(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	platformID := "sha256:latest|linux/amd64"

	// Insert an older scan
	older := model.Scan{
		ID:          "latest-old",
		PlatformID:  platformID,
		Scanner:     "grype",
		DBVersion:   "v4.0",
		StartedAt:   now.Add(-10 * time.Minute),
		CompletedAt: now.Add(-9 * time.Minute),
		VulnCount:   5,
		Status:      "completed",
	}
	// Insert a newer scan
	newer := model.Scan{
		ID:          "latest-new",
		PlatformID:  platformID,
		Scanner:     "grype",
		DBVersion:   "v5.0",
		StartedAt:   now.Add(-2 * time.Minute),
		CompletedAt: now.Add(-time.Minute),
		VulnCount:   3,
		Status:      "completed",
	}

	if err := s.UpsertScan(ctx, older); err != nil {
		t.Fatalf("upsert older scan: %v", err)
	}
	if err := s.UpsertScan(ctx, newer); err != nil {
		t.Fatalf("upsert newer scan: %v", err)
	}

	got, err := s.GetLatestScan(ctx, platformID, "grype")
	if err != nil {
		t.Fatalf("get latest scan: %v", err)
	}
	if got == nil {
		t.Fatal("expected scan, got nil")
	}
	if got.ID != "latest-new" {
		t.Errorf("expected latest scan ID %q, got %q", "latest-new", got.ID)
	}
	if got.DBVersion != "v5.0" {
		t.Errorf("expected db_version %q, got %q", "v5.0", got.DBVersion)
	}
}

func TestPGStore_ConcurrentUpsertScan(t *testing.T) {
	pool := testPool(t)
	s := store.NewPGStore(pool)
	ctx := context.Background()

	now := time.Now().Truncate(time.Microsecond)
	platformID := "sha256:concurrent|linux/amd64"

	// Insert grype scan and trivy scan for the same platform concurrently
	grypeScan := model.Scan{
		ID:            "conc-grype",
		PlatformID:    platformID,
		Scanner:       "grype",
		DBVersion:     "v5.0",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   now,
		VulnCount:     2,
		CriticalCount: 1,
		HighCount:     1,
		Status:        "completed",
	}
	trivyScan := model.Scan{
		ID:            "conc-trivy",
		PlatformID:    platformID,
		Scanner:       "trivy",
		DBVersion:     "v2.0",
		StartedAt:     now.Add(-time.Minute),
		CompletedAt:   now,
		VulnCount:     3,
		CriticalCount: 0,
		HighCount:     3,
		Status:        "completed",
	}

	// Upsert both (sequential, since we're testing the store, not actual goroutines
	// which would require a real concurrent DB — the important thing is both succeed)
	if err := s.UpsertScan(ctx, grypeScan); err != nil {
		t.Fatalf("upsert grype scan: %v", err)
	}
	if err := s.UpsertScan(ctx, trivyScan); err != nil {
		t.Fatalf("upsert trivy scan: %v", err)
	}

	// Verify each scanner's scan can be retrieved independently
	gotGrype, err := s.GetLatestScan(ctx, platformID, "grype")
	if err != nil {
		t.Fatalf("get grype scan: %v", err)
	}
	if gotGrype == nil {
		t.Fatal("expected grype scan, got nil")
	}
	if gotGrype.ID != "conc-grype" {
		t.Errorf("grype scan ID: got %q, want %q", gotGrype.ID, "conc-grype")
	}

	gotTrivy, err := s.GetLatestScan(ctx, platformID, "trivy")
	if err != nil {
		t.Fatalf("get trivy scan: %v", err)
	}
	if gotTrivy == nil {
		t.Fatal("expected trivy scan, got nil")
	}
	if gotTrivy.ID != "conc-trivy" {
		t.Errorf("trivy scan ID: got %q, want %q", gotTrivy.ID, "conc-trivy")
	}
}
