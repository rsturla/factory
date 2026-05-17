package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rsturla/factory/services/scan-service/internal/api"
	"github.com/rsturla/factory/services/scan-service/internal/model"
)

func setupBenchServer(b *testing.B, ms *mockStore) *http.ServeMux {
	b.Helper()
	srv := api.NewServer(ms)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	return mux
}

func BenchmarkGetFindings(b *testing.B) {
	ms := newMockStore()
	ms.findings["sha256:bench|grype"] = []model.Finding{
		{VulnID: "CVE-2024-0001", Severity: "Critical", PackageName: "glibc", PackageVersion: "2.39"},
		{VulnID: "CVE-2024-0002", Severity: "High", PackageName: "openssl", PackageVersion: "3.0.1"},
		{VulnID: "CVE-2024-0003", Severity: "Medium", PackageName: "curl", PackageVersion: "8.0"},
	}
	mux := setupBenchServer(b, ms)

	req := httptest.NewRequest("GET", "/api/v1/findings/sha256:bench?scanner=grype", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}

func BenchmarkGetScans(b *testing.B) {
	ms := newMockStore()
	ms.scans["sha256:bench|grype"] = &model.Scan{
		ID: "s1", PlatformID: "sha256:bench", Scanner: "grype",
		DBVersion: "v5.0", VulnCount: 10,
		CriticalCount: 2, HighCount: 3, MediumCount: 4, LowCount: 1,
		StartedAt: time.Now(), CompletedAt: time.Now(), Status: "completed",
	}
	mux := setupBenchServer(b, ms)

	req := httptest.NewRequest("GET", "/api/v1/scans/sha256:bench", nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status %d", rec.Code)
		}
	}
}
