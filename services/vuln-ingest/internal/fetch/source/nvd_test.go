package source_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
)

func gzipJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Write(raw) //nolint:errcheck
	gz.Close()
	return buf.Bytes()
}

func makeFeed(t *testing.T, cves ...string) []byte {
	t.Helper()
	var vulns []map[string]any
	for _, id := range cves {
		vulns = append(vulns, map[string]any{
			"cve": map[string]any{
				"id":           id,
				"published":    "2024-01-15T00:00:00.000",
				"lastModified": "2024-06-01T00:00:00.000",
				"vulnStatus":   "Analyzed",
				"descriptions": []map[string]string{{"lang": "en", "value": "Test vuln " + id}},
				"metrics":      map[string]any{},
			},
		})
	}
	return gzipJSON(t, map[string]any{"vulnerabilities": vulns})
}

func makeCheckpoint(t *testing.T, etags map[string]string) string {
	t.Helper()
	cp, _ := json.Marshal(map[string]any{"etags": etags})
	return string(cp)
}

func testBlobStore(t *testing.T) blob.Store {
	t.Helper()
	s, err := blob.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNVDSource_Bootstrap(t *testing.T) {
	feed2024 := makeFeed(t, "CVE-2024-0001", "CVE-2024-0002")
	feed2025 := makeFeed(t, "CVE-2025-0001")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"etag-`+r.URL.Path+`"`)
		if r.Method == http.MethodHead {
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "2024.json.gz"):
			w.Write(feed2024) //nolint:errcheck
		case strings.Contains(r.URL.Path, "2025.json.gz"):
			w.Write(feed2025) //nolint:errcheck
		default:
			w.Write(makeFeed(t)) //nolint:errcheck
		}
	}))
	defer srv.Close()

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	result, err := s.Fetch(context.Background(), blobs, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if result.ItemCount < 3 {
		t.Errorf("items: got %d, want >= 3", result.ItemCount)
	}
	data, getErr := blobs.Get(context.Background(), "nvd/CVE-2024-0001.json")
	if getErr != nil {
		t.Fatal("CVE-2024-0001.json not written")
	}
	var cve struct{ ID string `json:"id"` }
	if err := json.Unmarshal(data, &cve); err != nil || cve.ID != "CVE-2024-0001" {
		t.Errorf("CVE-2024-0001 content: got id=%q, err=%v", cve.ID, err)
	}
	if result.NewCheckpoint == "" {
		t.Error("checkpoint empty after bootstrap")
	}
}

func TestNVDSource_OngoingNoChanges(t *testing.T) {
	etags := make(map[string]string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label := feedLabel(r.URL.Path)
		w.Header().Set("Etag", `"same-`+label+`"`)
		if r.Method == http.MethodHead {
			return
		}
		w.Write(makeFeed(t)) //nolint:errcheck
	}))
	defer srv.Close()

	// Build checkpoint with matching etags for all feeds.
	for year := 2002; year <= 2026; year++ {
		etags[strconv.Itoa(year)] = `"same-` + strconv.Itoa(year) + `"`
	}
	etags["modified"] = `"same-modified"`

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	result, err := s.Fetch(context.Background(), blobs, makeCheckpoint(t, etags))
	if err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	if len(result.ChangedFiles) != 0 {
		t.Errorf("expected no changes, got %d", len(result.ChangedFiles))
	}
}

func TestNVDSource_OngoingOneYearChanged(t *testing.T) {
	etags := make(map[string]string)
	feed2023 := makeFeed(t, "CVE-2023-9999")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label := feedLabel(r.URL.Path)
		if label == "2023" {
			w.Header().Set("Etag", `"new-2023"`)
		} else {
			w.Header().Set("Etag", `"same-`+label+`"`)
		}
		if r.Method == http.MethodHead {
			return
		}
		if strings.Contains(r.URL.Path, "2023") {
			w.Write(feed2023) //nolint:errcheck
		} else {
			w.Write(makeFeed(t)) //nolint:errcheck
		}
	}))
	defer srv.Close()

	for year := 2002; year <= 2026; year++ {
		etags[strconv.Itoa(year)] = `"same-` + strconv.Itoa(year) + `"`
	}
	etags["modified"] = `"same-modified"`

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	result, err := s.Fetch(context.Background(), blobs, makeCheckpoint(t, etags))
	if err != nil {
		t.Fatalf("one year changed: %v", err)
	}
	if result.ItemCount != 1 {
		t.Errorf("items: got %d, want 1", result.ItemCount)
	}
	if _, err := blobs.Get(context.Background(), "nvd/CVE-2023-9999.json"); err != nil {
		t.Error("CVE-2023-9999.json not written")
	}
}

func TestNVDSource_ModifiedFeedChanged(t *testing.T) {
	etags := make(map[string]string)
	modFeed := makeFeed(t, "CVE-2020-1111")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		label := feedLabel(r.URL.Path)
		if label == "modified" {
			w.Header().Set("Etag", `"mod-new"`)
		} else {
			w.Header().Set("Etag", `"same-`+label+`"`)
		}
		if r.Method == http.MethodHead {
			return
		}
		if strings.Contains(r.URL.Path, "modified") {
			w.Write(modFeed) //nolint:errcheck
		} else {
			w.Write(makeFeed(t)) //nolint:errcheck
		}
	}))
	defer srv.Close()

	for year := 2002; year <= 2026; year++ {
		etags[strconv.Itoa(year)] = `"same-` + strconv.Itoa(year) + `"`
	}
	etags["modified"] = `"mod-old"`

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	result, err := s.Fetch(context.Background(), blobs, makeCheckpoint(t, etags))
	if err != nil {
		t.Fatalf("modified changed: %v", err)
	}
	if result.ItemCount != 1 {
		t.Errorf("items: got %d, want 1", result.ItemCount)
	}
}

func TestNVDSource_FeedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	_, err := s.Fetch(context.Background(), blobs, "")
	if err == nil {
		t.Fatal("expected error on 503")
	}
}

func TestNVDSource_CVEFileContent(t *testing.T) {
	feed := makeFeed(t, "CVE-2024-5555")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Etag", `"e"`)
		if r.Method == http.MethodHead {
			return
		}
		w.Write(feed) //nolint:errcheck
	}))
	defer srv.Close()

	s := source.NewNVDSourceWithURL(srv.URL)
	blobs := testBlobStore(t)

	s.Fetch(context.Background(), blobs, makeCheckpoint(t, map[string]string{"modified": `"old"`})) //nolint:errcheck

	data, err := blobs.Get(context.Background(), "nvd/CVE-2024-5555.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var cve struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &cve); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cve.ID != "CVE-2024-5555" {
		t.Errorf("id: got %q, want CVE-2024-5555", cve.ID)
	}
}

func feedLabel(path string) string {
	if strings.Contains(path, "modified") {
		return "modified"
	}
	for year := 2002; year <= 2030; year++ {
		if strings.Contains(path, strconv.Itoa(year)) {
			return strconv.Itoa(year)
		}
	}
	return "unknown"
}
