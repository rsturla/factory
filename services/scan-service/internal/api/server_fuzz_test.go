package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/rsturla/factory/services/scan-service/internal/api"
)

func FuzzFindingsEndpoint(f *testing.F) {
	f.Add("sha256:abc", "grype", "critical")
	f.Add("", "", "")
	f.Add("sha256:abc", "", "high")
	f.Add("sha256:abc/linux/amd64", "grype", "")
	f.Add("sha256:abc/linux/amd64", "", "medium")
	f.Add("sha256:abc/linux/arm64/v8", "trivy", "low")

	f.Fuzz(func(t *testing.T, platformID, scanner, severity string) {
		ms := newMockStore()
		srv := api.NewServer(ms)
		mux := http.NewServeMux()
		srv.RegisterRoutes(mux)

		// Use url.PathEscape to ensure the path is valid for http.NewRequest
		path := "/api/v1/findings/" + url.PathEscape(platformID)
		q := url.Values{}
		if scanner != "" {
			q.Set("scanner", scanner)
		}
		if severity != "" {
			q.Set("severity", severity)
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}

		req, err := http.NewRequest("GET", path, nil)
		if err != nil {
			// Skip inputs that produce invalid HTTP requests
			return
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// Should return a valid HTTP status, not panic
		if rec.Code < 100 || rec.Code >= 600 {
			t.Errorf("invalid HTTP status code: %d", rec.Code)
		}
	})
}

func FuzzScansEndpoint(f *testing.F) {
	f.Add("sha256:abc/linux/amd64")
	f.Add("")
	f.Add("sha256:abc")
	f.Add("no-pipe")
	f.Add("sha256:abc/linux/arm64/v8")

	f.Fuzz(func(t *testing.T, platformID string) {
		ms := newMockStore()
		srv := api.NewServer(ms)
		mux := http.NewServeMux()
		srv.RegisterRoutes(mux)

		path := "/api/v1/scans/" + url.PathEscape(platformID)
		req, err := http.NewRequest("GET", path, nil)
		if err != nil {
			return
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		// Should return a valid HTTP status, not panic
		if rec.Code < 100 || rec.Code >= 600 {
			t.Errorf("invalid HTTP status code: %d", rec.Code)
		}
	})
}
