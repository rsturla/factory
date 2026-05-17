package source_test

import (
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
)

// ---------------------------------------------------------------------------
// filterByGlob tests
// ---------------------------------------------------------------------------

func TestFilterByGlob_EmptyGlob(t *testing.T) {
	files := []string{"a.json", "b.txt", "c.csv"}
	got := source.FilterByGlob(files, "")
	if len(got) != len(files) {
		t.Fatalf("empty glob: expected %d files, got %d", len(files), len(got))
	}
	for i, f := range files {
		if got[i] != f {
			t.Fatalf("expected %q at index %d, got %q", f, i, got[i])
		}
	}
}

func TestFilterByGlob_StarGlob(t *testing.T) {
	files := []string{"a.json", "b.txt"}
	got := source.FilterByGlob(files, "*")
	if len(got) != len(files) {
		t.Fatalf("star glob: expected %d files, got %d", len(files), len(got))
	}
}

func TestFilterByGlob_JSONOnly(t *testing.T) {
	files := []string{
		"advisories/GHSA-1234.json",
		"advisories/GHSA-5678.json",
		"README.md",
		"data/notes.txt",
		"index.html",
	}
	got := source.FilterByGlob(files, "*.json")
	if len(got) != 2 {
		t.Fatalf("expected 2 .json files, got %d: %v", len(got), got)
	}
	for _, f := range got {
		if len(f) < 5 || f[len(f)-5:] != ".json" {
			t.Fatalf("expected .json extension, got %q", f)
		}
	}
}

func TestFilterByGlob_NoMatches(t *testing.T) {
	files := []string{"a.txt", "b.csv", "c.xml"}
	got := source.FilterByGlob(files, "*.json")
	if len(got) != 0 {
		t.Fatalf("expected 0 matches, got %d: %v", len(got), got)
	}
}

func TestFilterByGlob_EmptyInput(t *testing.T) {
	got := source.FilterByGlob(nil, "*.json")
	if len(got) != 0 {
		t.Fatalf("expected 0 results for nil input, got %d", len(got))
	}

	got = source.FilterByGlob([]string{}, "*.json")
	if len(got) != 0 {
		t.Fatalf("expected 0 results for empty input, got %d", len(got))
	}
}

func TestFilterByGlob_SkipsEmptyStrings(t *testing.T) {
	files := []string{"", "a.json", "", "b.json", ""}
	got := source.FilterByGlob(files, "*.json")
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d: %v", len(got), got)
	}
}

func TestFilterByGlob_SubdirPaths(t *testing.T) {
	files := []string{
		"2024/CVE-2024-0001.json",
		"2024/CVE-2024-0002.json",
		"2024/CVE-2024-0003.txt",
		"delta.log",
	}
	got := source.FilterByGlob(files, "*.json")
	if len(got) != 2 {
		t.Fatalf("expected 2 .json files in subdirs, got %d: %v", len(got), got)
	}
}

func TestFilterByGlob_CVEPrefixRejectsDeltaLog(t *testing.T) {
	files := []string{
		"2024/1xxx/CVE-2024-1234.json",
		"2024/2xxx/CVE-2024-2000.json",
		"deltaLog.json",
		"README.md",
	}
	got := source.FilterByGlob(files, "CVE-*.json")
	if len(got) != 2 {
		t.Fatalf("expected 2 CVE files, got %d: %v", len(got), got)
	}
	for _, f := range got {
		base := f[len(f)-len("CVE-2024-1234.json"):]
		if base[:4] != "CVE-" {
			t.Fatalf("expected CVE- prefix, got %q", f)
		}
	}
}
