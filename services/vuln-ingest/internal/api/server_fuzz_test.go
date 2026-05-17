package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/api"
)

func fuzzMux(t *testing.T) *http.ServeMux {
	t.Helper()
	s := api.NewServer(&mockStore{})
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	return mux
}

func FuzzBatchGet(f *testing.F) {
	f.Add([]byte(`{"ids":["CVE-2024-0001"]}`))
	f.Add([]byte(`{"ids":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		mux := fuzzMux(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/vulns:batchGet", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status: %d", rec.Code)
		}
	})
}

func FuzzGetVuln(f *testing.F) {
	f.Add("CVE-2024-0001")
	f.Add("..%2F..%2Fetc%2Fpasswd")
	f.Add("GHSA-xxxx-yyyy-zzzz")
	f.Add("a]b[c")

	f.Fuzz(func(t *testing.T, id string) {
		mux := fuzzMux(t)
		escaped := url.PathEscape(id)
		req := httptest.NewRequest(http.MethodGet, "/v1/vulns/"+escaped, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status: %d", rec.Code)
		}
	})
}

func FuzzListVulns(f *testing.F) {
	f.Add("100", "0", "")
	f.Add("abc", "-1", "not-a-date")
	f.Add("999999", "0", "2024-01-01T00:00:00Z")
	f.Add("0", "0", "")

	f.Fuzz(func(t *testing.T, limit, offset, modifiedSince string) {
		mux := fuzzMux(t)
		params := url.Values{}
		params.Set("limit", limit)
		params.Set("offset", offset)
		if modifiedSince != "" {
			params.Set("modified_since", modifiedSince)
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/vulns?"+params.Encode(), nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status: %d", rec.Code)
		}
	})
}

func FuzzBatchQueryAffected(f *testing.F) {
	f.Add([]byte(`{"queries":[{"package_name":"express"}]}`))
	f.Add([]byte(`{"queries":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
	f.Add([]byte(`{"queries":[{"ecosystem":"Go","package_name":"example.com/lib","purl":"pkg:golang/example.com/lib"}]}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		mux := fuzzMux(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/affected:batchQuery", bytes.NewReader(data))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code < 100 || rec.Code > 599 {
			t.Errorf("invalid status: %d", rec.Code)
		}
	})
}
