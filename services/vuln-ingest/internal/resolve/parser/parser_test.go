package parser_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/resolve/parser"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		parsed, err = time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("mustTime: cannot parse %q: %v", s, err)
		}
	}
	return parsed
}

func requireLen(t *testing.T, label string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got len %d, want %d", label, got, want)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func requireNonNil(t *testing.T, label string, v *time.Time) {
	t.Helper()
	if v == nil {
		t.Fatalf("%s: expected non-nil time, got nil", label)
	}
}

func requireNil(t *testing.T, label string, v *time.Time) {
	t.Helper()
	if v != nil {
		t.Fatalf("%s: expected nil time, got %v", label, *v)
	}
}

func containsFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// parseTime (exported via Parse round-trip; tested via OSV minimal fixture)
// We use a minimal OSV record to probe parseTime through the public API,
// since parseTime is unexported. Each sub-test feeds a different time format
// into the published / modified / withdrawn fields.
// ---------------------------------------------------------------------------

func TestParseTime_Formats(t *testing.T) {
	tests := []struct {
		name      string
		timeStr   string
		wantNil   bool
		wantYear  int
		wantMonth time.Month
		wantDay   int
	}{
		{
			name:    "empty string",
			timeStr: "",
			wantNil: true,
		},
		{
			name:      "RFC3339",
			timeStr:   "2024-06-15T10:30:00Z",
			wantYear:  2024,
			wantMonth: time.June,
			wantDay:   15,
		},
		{
			name:      "RFC3339Nano",
			timeStr:   "2024-06-15T10:30:00.123456789Z",
			wantYear:  2024,
			wantMonth: time.June,
			wantDay:   15,
		},
		{
			name:      "date only",
			timeStr:   "2025-01-20",
			wantYear:  2025,
			wantMonth: time.January,
			wantDay:   20,
		},
		{
			name:      "datetime no TZ",
			timeStr:   "2025-03-10T14:22:33",
			wantYear:  2025,
			wantMonth: time.March,
			wantDay:   10,
		},
		{
			name:    "invalid string",
			timeStr: "not-a-date",
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Feed the time string as the "published" field of a minimal OSV record.
			json := `{"id":"PARSE-TIME-TEST","published":"` + tc.timeStr + `"}`
			p := &parser.OSVParser{}
			vulns, err := p.Parse([]byte(json))
			requireNoError(t, err)
			requireLen(t, "vulns", len(vulns), 1)

			got := vulns[0].Published
			if tc.wantNil {
				requireNil(t, "Published", got)
				return
			}
			requireNonNil(t, "Published", got)
			if got.Year() != tc.wantYear {
				t.Errorf("year: got %d, want %d", got.Year(), tc.wantYear)
			}
			if got.Month() != tc.wantMonth {
				t.Errorf("month: got %v, want %v", got.Month(), tc.wantMonth)
			}
			if got.Day() != tc.wantDay {
				t.Errorf("day: got %d, want %d", got.Day(), tc.wantDay)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// OSVParser
// ---------------------------------------------------------------------------

func TestOSVParser_MinimalRecord(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-1234-5678-abcd",
		"summary": "XSS in widget renderer",
		"modified": "2025-01-10T08:00:00Z"
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	if v.ID != "GHSA-1234-5678-abcd" {
		t.Errorf("ID: got %q, want %q", v.ID, "GHSA-1234-5678-abcd")
	}
	if v.Summary != "XSS in widget renderer" {
		t.Errorf("Summary: got %q, want %q", v.Summary, "XSS in widget renderer")
	}
	requireNonNil(t, "Modified", v.Modified)
	requireNil(t, "Published", v.Published)
	requireNil(t, "Withdrawn", v.Withdrawn)
}

func TestOSVParser_FullRecord(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-full-test-0001",
		"aliases": ["CVE-2025-12345"],
		"summary": "SQL injection in query builder",
		"details": "The query builder does not sanitize user input when constructing WHERE clauses.",
		"modified": "2025-02-20T12:00:00Z",
		"published": "2025-02-18T09:00:00Z",
		"severity": [
			{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:N"}
		],
		"affected": [
			{
				"package": {
					"ecosystem": "npm",
					"name": "@acme/query-builder",
					"purl": "pkg:npm/%40acme/query-builder"
				},
				"ranges": [
					{
						"type": "SEMVER",
						"events": [
							{"introduced": "1.0.0"},
							{"fixed": "1.4.2"}
						]
					}
				],
				"versions": ["1.0.0","1.1.0","1.2.0","1.3.0","1.4.0","1.4.1"],
				"database_specific": {"severity": "HIGH"}
			}
		],
		"references": [
			{"type": "ADVISORY", "url": "https://github.com/acme/query-builder/security/advisories/GHSA-full-test-0001"},
			{"type": "FIX", "url": "https://github.com/acme/query-builder/commit/abc123"}
		],
		"database_specific": {"github_reviewed": true}
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	if v.ID != "GHSA-full-test-0001" {
		t.Errorf("ID: got %q", v.ID)
	}
	requireLen(t, "aliases", len(v.Aliases), 1)
	if v.Aliases[0] != "CVE-2025-12345" {
		t.Errorf("alias[0]: got %q", v.Aliases[0])
	}
	if v.Details == "" {
		t.Error("Details should not be empty")
	}
	requireNonNil(t, "Published", v.Published)
	requireNonNil(t, "Modified", v.Modified)

	// Severity
	requireLen(t, "severity", len(v.Severity), 1)
	if v.Severity[0].Type != "CVSS_V3" {
		t.Errorf("severity type: got %q", v.Severity[0].Type)
	}

	// References
	requireLen(t, "references", len(v.References), 2)
	if v.References[0].Type != "ADVISORY" {
		t.Errorf("ref[0] type: got %q", v.References[0].Type)
	}

	// Affected packages
	requireLen(t, "affected", len(v.AffectedPackages), 1)
	ap := v.AffectedPackages[0]
	if ap.Ecosystem != "npm" {
		t.Errorf("ecosystem: got %q", ap.Ecosystem)
	}
	if ap.PackageName != "@acme/query-builder" {
		t.Errorf("package name: got %q", ap.PackageName)
	}
	if ap.Purl != "pkg:npm/%40acme/query-builder" {
		t.Errorf("purl: got %q", ap.Purl)
	}
	requireLen(t, "versions", len(ap.Versions), 6)
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].Introduced != "1.0.0" {
		t.Errorf("introduced: got %q", ap.VersionRanges[0].Introduced)
	}
	if ap.VersionRanges[0].Fixed != "1.4.2" {
		t.Errorf("fixed: got %q", ap.VersionRanges[0].Fixed)
	}

	// DatabaseSpecific populated
	if v.DatabaseSpecific == nil {
		t.Error("DatabaseSpecific should not be nil")
	}
}

func TestOSVParser_MultipleAffectedPackages(t *testing.T) {
	data := []byte(`{
		"id": "RUSTSEC-2025-0042",
		"modified": "2025-03-01T00:00:00Z",
		"affected": [
			{
				"package": {"ecosystem": "crates.io", "name": "libfoo"},
				"ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "0.5.1"}]}]
			},
			{
				"package": {"ecosystem": "crates.io", "name": "libbar"},
				"ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "1.2.0"}]}]
			}
		]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 2)

	if vulns[0].AffectedPackages[0].PackageName != "libfoo" {
		t.Errorf("first package: got %q", vulns[0].AffectedPackages[0].PackageName)
	}
	if vulns[0].AffectedPackages[1].PackageName != "libbar" {
		t.Errorf("second package: got %q", vulns[0].AffectedPackages[1].PackageName)
	}
}

func TestOSVParser_QualityFlag_UnboundedRange(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-unbnd-0001",
		"modified": "2025-04-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "PyPI", "name": "vulnerable-lib"},
			"ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}]}]
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "unbounded_range") {
		t.Errorf("expected unbounded_range flag, got %v", ap.QualityFlags)
	}
	// introduced="0" also triggers no_upper_bound since there is no fixed/lastAffected
	if !containsFlag(ap.QualityFlags, "no_upper_bound") {
		t.Errorf("expected no_upper_bound flag, got %v", ap.QualityFlags)
	}
}

func TestOSVParser_QualityFlag_NoUpperBound(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-noub-0001",
		"modified": "2025-04-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "Go", "name": "example.com/vuln"},
			"ranges": [{"type": "SEMVER", "events": [{"introduced": "1.3.0"}]}]
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "no_upper_bound") {
		t.Errorf("expected no_upper_bound flag, got %v", ap.QualityFlags)
	}
}

func TestOSVParser_QualityFlag_EmptyRange(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-empty-0001",
		"modified": "2025-04-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "npm", "name": "empty-range-pkg"}
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "empty_range") {
		t.Errorf("expected empty_range flag, got %v", ap.QualityFlags)
	}
}

func TestOSVParser_LimitEvent(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-limit-0001",
		"modified": "2025-04-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "npm", "name": "limit-pkg"},
			"ranges": [{"type": "SEMVER", "events": [
				{"introduced": "0"},
				{"limit": "2.0.0"}
			]}]
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	// limit maps to Fixed
	if ap.VersionRanges[0].Fixed != "2.0.0" {
		t.Errorf("Fixed (from limit): got %q, want %q", ap.VersionRanges[0].Fixed, "2.0.0")
	}
}

func TestOSVParser_MultipleEventsWithResets(t *testing.T) {
	// Two introduced-fixed pairs in a single range
	data := []byte(`{
		"id": "GHSA-multi-0001",
		"modified": "2025-04-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "Maven", "name": "com.example:multi-range"},
			"ranges": [{"type": "ECOSYSTEM", "events": [
				{"introduced": "1.0.0"},
				{"fixed": "1.0.5"},
				{"introduced": "2.0.0"},
				{"fixed": "2.0.3"}
			]}]
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 2)

	if ap.VersionRanges[0].Introduced != "1.0.0" || ap.VersionRanges[0].Fixed != "1.0.5" {
		t.Errorf("range[0]: got %+v", ap.VersionRanges[0])
	}
	if ap.VersionRanges[1].Introduced != "2.0.0" || ap.VersionRanges[1].Fixed != "2.0.3" {
		t.Errorf("range[1]: got %+v", ap.VersionRanges[1])
	}
}

func TestOSVParser_AliasesAndSeverity(t *testing.T) {
	data := []byte(`{
		"id": "PYSEC-2025-001",
		"aliases": ["CVE-2025-99999", "GHSA-xxxx-yyyy-zzzz"],
		"modified": "2025-04-01T00:00:00Z",
		"severity": [
			{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"},
			{"type": "CVSS_V4", "score": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:N/VA:N/SC:N/SI:N/SA:N"}
		]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	v := vulns[0]
	requireLen(t, "aliases", len(v.Aliases), 2)
	requireLen(t, "severity", len(v.Severity), 2)
	if v.Severity[0].Type != "CVSS_V3" {
		t.Errorf("severity[0] type: got %q", v.Severity[0].Type)
	}
	if v.Severity[1].Type != "CVSS_V4" {
		t.Errorf("severity[1] type: got %q", v.Severity[1].Type)
	}
}

func TestOSVParser_References(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-refs-0001",
		"modified": "2025-04-01T00:00:00Z",
		"references": [
			{"type": "ADVISORY", "url": "https://nvd.nist.gov/vuln/detail/CVE-2025-99999"},
			{"type": "FIX", "url": "https://github.com/acme/lib/commit/deadbeef"},
			{"type": "WEB", "url": "https://blog.acme.com/disclosure"}
		]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "references", len(vulns[0].References), 3)
	if vulns[0].References[0].Type != "ADVISORY" {
		t.Errorf("ref[0] type: got %q", vulns[0].References[0].Type)
	}
	if vulns[0].References[1].URL != "https://github.com/acme/lib/commit/deadbeef" {
		t.Errorf("ref[1] URL: got %q", vulns[0].References[1].URL)
	}
}

func TestOSVParser_Withdrawn(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-with-draw-0001",
		"modified": "2025-05-01T00:00:00Z",
		"withdrawn": "2025-04-28T12:00:00Z"
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireNonNil(t, "Withdrawn", vulns[0].Withdrawn)
	want := mustTime(t, "2025-04-28T12:00:00Z")
	if !vulns[0].Withdrawn.Equal(want) {
		t.Errorf("Withdrawn: got %v, want %v", *vulns[0].Withdrawn, want)
	}
}

// ---------------------------------------------------------------------------
// NVDParser
// ---------------------------------------------------------------------------

func TestNVDParser_ValidRecord(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-10001",
		"published": "2025-03-15T08:00:00.000",
		"lastModified": "2025-03-20T10:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [
			{"lang": "en", "value": "A buffer overflow in libfoo allows remote attackers to execute arbitrary code via crafted input."},
			{"lang": "es", "value": "Un desbordamiento de buffer en libfoo permite..."}
		],
		"metrics": {
			"cvssMetricV31": [{
				"source": "nvd@nist.gov",
				"type": "Primary",
				"cvssData": {
					"version": "3.1",
					"vectorString": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
					"baseScore": 9.8
				}
			}]
		},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:acme:libfoo:*:*:*:*:*:*:*:*",
					"versionStartIncluding": "1.0.0",
					"versionEndExcluding": "1.5.3"
				}]
			}]
		}],
		"references": [
			{"url": "https://acme.com/advisory/2025-001", "source": "cve@mitre.org", "tags": ["Vendor Advisory"]},
			{"url": "https://github.com/acme/libfoo/commit/abc123", "source": "cve@mitre.org", "tags": ["Patch"]}
		],
		"weaknesses": [{
			"source": "nvd@nist.gov",
			"type": "Primary",
			"description": [{"lang": "en", "value": "CWE-120"}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	if v.ID != "CVE-2025-10001" {
		t.Errorf("ID: got %q", v.ID)
	}
	requireNonNil(t, "Published", v.Published)
	requireNonNil(t, "Modified", v.Modified)
	requireNil(t, "Withdrawn", v.Withdrawn)

	// English description used
	if !strings.Contains(v.Details, "buffer overflow") {
		t.Errorf("Details should contain English description, got %q", v.Details)
	}
	if v.Summary == "" {
		t.Error("Summary should not be empty")
	}

	// Severity
	requireLen(t, "severity", len(v.Severity), 1)
	if v.Severity[0].Type != "CVSS_V3_1" {
		t.Errorf("severity type: got %q, want CVSS_V3_1", v.Severity[0].Type)
	}
	if v.Severity[0].Score != "9.8" {
		t.Errorf("severity score: got %q, want 9.8", v.Severity[0].Score)
	}
	if v.Severity[0].Vector != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H" {
		t.Errorf("severity vector: got %q", v.Severity[0].Vector)
	}

	// References
	requireLen(t, "references", len(v.References), 2)
	if v.References[0].Type != "ADVISORY" {
		t.Errorf("ref[0] type: got %q, want ADVISORY", v.References[0].Type)
	}
	if v.References[1].Type != "FIX" {
		t.Errorf("ref[1] type: got %q, want FIX", v.References[1].Type)
	}

	// Affected packages from CPE
	requireLen(t, "affected", len(v.AffectedPackages), 1)
	ap := v.AffectedPackages[0]
	if ap.PackageName != "libfoo" {
		t.Errorf("package: got %q", ap.PackageName)
	}
	if ap.Vendor != "acme" {
		t.Errorf("vendor: got %q, want acme", ap.Vendor)
	}
	if ap.Ecosystem != "" {
		t.Errorf("ecosystem: got %q, want empty", ap.Ecosystem)
	}
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].Introduced != "1.0.0" {
		t.Errorf("introduced: got %q", ap.VersionRanges[0].Introduced)
	}
	if ap.VersionRanges[0].Fixed != "1.5.3" {
		t.Errorf("fixed: got %q", ap.VersionRanges[0].Fixed)
	}

	// CWE extraction
	dbSpec, ok := v.DatabaseSpecific.(map[string]any)
	if !ok {
		t.Fatal("DatabaseSpecific should be map[string]any")
	}
	cwes, ok := dbSpec["cwes"].([]string)
	if !ok {
		t.Fatal("cwes should be []string")
	}
	requireLen(t, "cwes", len(cwes), 1)
	if cwes[0] != "CWE-120" {
		t.Errorf("cwe: got %q", cwes[0])
	}
}

func TestNVDParser_RejectedCVE(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-99999",
		"published": "2025-01-01T00:00:00.000",
		"lastModified": "2025-02-01T00:00:00.000",
		"vulnStatus": "Rejected",
		"descriptions": [{"lang": "en", "value": "This CVE was rejected."}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	requireNonNil(t, "Withdrawn", v.Withdrawn)
	// Rejected: Withdrawn == Modified
	if !v.Withdrawn.Equal(*v.Modified) {
		t.Errorf("Withdrawn should equal Modified for rejected CVE")
	}
	// Details should be empty for rejected
	if v.Details != "" {
		t.Errorf("Details should be empty for rejected CVE, got %q", v.Details)
	}
}

func TestNVDParser_CVSSV2(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-20001",
		"published": "2025-01-10T00:00:00.000",
		"lastModified": "2025-01-15T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Test CVE for CVSS V2 scoring."}],
		"metrics": {
			"cvssMetricV2": [{
				"source": "nvd@nist.gov",
				"type": "Primary",
				"cvssV2": {
					"version": "2.0",
					"vectorString": "AV:N/AC:L/Au:N/C:P/I:P/A:P",
					"baseScore": 7.5
				}
			}]
		}
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "severity", len(vulns[0].Severity), 1)
	if vulns[0].Severity[0].Type != "CVSS_V2_0" {
		t.Errorf("severity type: got %q, want CVSS_V2_0", vulns[0].Severity[0].Type)
	}
	if vulns[0].Severity[0].Score != "7.5" {
		t.Errorf("severity score: got %q, want 7.5", vulns[0].Severity[0].Score)
	}
}

func TestNVDParser_CVSSV30(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-20002",
		"published": "2025-01-10T00:00:00.000",
		"lastModified": "2025-01-15T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Test CVE for CVSS V3.0 scoring."}],
		"metrics": {
			"cvssMetricV30": [{
				"source": "nvd@nist.gov",
				"type": "Primary",
				"cvssData": {
					"version": "3.0",
					"vectorString": "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N",
					"baseScore": 7.5
				}
			}]
		}
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "severity", len(vulns[0].Severity), 1)
	if vulns[0].Severity[0].Type != "CVSS_V3_0" {
		t.Errorf("severity type: got %q, want CVSS_V3_0", vulns[0].Severity[0].Type)
	}
}

func TestNVDParser_CVSSV40(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-20003",
		"published": "2025-01-10T00:00:00.000",
		"lastModified": "2025-01-15T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Test CVE for CVSS V4.0 scoring."}],
		"metrics": {
			"cvssMetricV40": [{
				"source": "nvd@nist.gov",
				"type": "Primary",
				"cvssData": {
					"version": "4.0",
					"vectorString": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H/SC:N/SI:N/SA:N",
					"baseScore": 9.3
				}
			}]
		}
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "severity", len(vulns[0].Severity), 1)
	if vulns[0].Severity[0].Type != "CVSS_V4_0" {
		t.Errorf("severity type: got %q, want CVSS_V4_0", vulns[0].Severity[0].Type)
	}
	if vulns[0].Severity[0].Score != "9.3" {
		t.Errorf("severity score: got %q, want 9.3", vulns[0].Severity[0].Score)
	}
}

func TestNVDParser_CPEVersionEndIncluding(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30001",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Version end including test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:vendor:product:*:*:*:*:*:*:*:*",
					"versionStartIncluding": "2.0.0",
					"versionEndIncluding": "2.9.9"
				}]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].Introduced != "2.0.0" {
		t.Errorf("introduced: got %q", ap.VersionRanges[0].Introduced)
	}
	if ap.VersionRanges[0].LastAffected != "2.9.9" {
		t.Errorf("lastAffected: got %q, want 2.9.9", ap.VersionRanges[0].LastAffected)
	}
	if ap.VersionRanges[0].Fixed != "" {
		t.Errorf("fixed should be empty, got %q", ap.VersionRanges[0].Fixed)
	}
}

func TestNVDParser_NoUpperBoundFlag(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30002",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "No upper bound test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:vendor:product:*:*:*:*:*:*:*:*",
					"versionStartIncluding": "3.0.0"
				}]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "no_upper_bound") {
		t.Errorf("expected no_upper_bound flag, got %v", ap.QualityFlags)
	}
}

func TestNVDParser_UnmappedCPEFlag(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30003",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Unmapped CPE test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:vendor:product:*:*:*:*:*:*:*:*"
				}]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "all_versions_affected") {
		t.Errorf("expected all_versions_affected flag, got %v", ap.QualityFlags)
	}
	if len(ap.VersionRanges) != 1 {
		t.Fatalf("expected 1 version range, got %d", len(ap.VersionRanges))
	}
	if ap.VersionRanges[0].Fixed != "*" {
		t.Errorf("expected fixed=*, got %q", ap.VersionRanges[0].Fixed)
	}
}

func TestNVDParser_CWEFiltersNoInfo(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30004",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "CWE filter test."}],
		"metrics": {},
		"weaknesses": [
			{
				"source": "nvd@nist.gov",
				"type": "Primary",
				"description": [
					{"lang": "en", "value": "NVD-CWE-noinfo"},
					{"lang": "en", "value": "CWE-79"}
				]
			},
			{
				"source": "nvd@nist.gov",
				"type": "Secondary",
				"description": [
					{"lang": "en", "value": "NVD-CWE-Other"},
					{"lang": "en", "value": "CWE-89"}
				]
			}
		]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	dbSpec, ok := vulns[0].DatabaseSpecific.(map[string]any)
	if !ok {
		t.Fatal("DatabaseSpecific should be map[string]any")
	}
	cwes, ok := dbSpec["cwes"].([]string)
	if !ok {
		t.Fatal("cwes should be []string")
	}
	// NVD-CWE-noinfo and NVD-CWE-Other should be filtered
	requireLen(t, "cwes", len(cwes), 2)
	if cwes[0] != "CWE-79" {
		t.Errorf("cwes[0]: got %q, want CWE-79", cwes[0])
	}
	if cwes[1] != "CWE-89" {
		t.Errorf("cwes[1]: got %q, want CWE-89", cwes[1])
	}
}

func TestNVDParser_ReferenceTypeMapping(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30005",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Ref type mapping test."}],
		"metrics": {},
		"references": [
			{"url": "https://example.com/patch", "source": "cve@mitre.org", "tags": ["Patch"]},
			{"url": "https://example.com/advisory", "source": "cve@mitre.org", "tags": ["Vendor Advisory"]},
			{"url": "https://example.com/exploit", "source": "cve@mitre.org", "tags": ["Exploit"]},
			{"url": "https://example.com/info", "source": "cve@mitre.org", "tags": ["Third Party Advisory"]}
		]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	refs := vulns[0].References
	requireLen(t, "references", len(refs), 4)

	wantTypes := []string{"FIX", "ADVISORY", "EVIDENCE", "WEB"}
	for i, want := range wantTypes {
		if refs[i].Type != want {
			t.Errorf("ref[%d] type: got %q, want %q", i, refs[i].Type, want)
		}
	}
}

func TestNVDParser_ENLanguagePreference(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30006",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [
			{"lang": "es", "value": "Descripcion en espanol."},
			{"lang": "en", "value": "English description selected."}
		],
		"metrics": {}
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	if vulns[0].Details != "English description selected." {
		t.Errorf("Details should use English description, got %q", vulns[0].Details)
	}
}

func TestNVDParser_CPEExactVersion(t *testing.T) {
	// CPE with a specific version (parts[5] != "*") and no range fields
	data := []byte(`{
		"id": "CVE-2025-30007",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Exact version test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:vendor:product:3.2.1:*:*:*:*:*:*:*"
				}]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "versions", len(ap.Versions), 1)
	if ap.Versions[0] != "3.2.1" {
		t.Errorf("version: got %q, want 3.2.1", ap.Versions[0])
	}
	requireLen(t, "version_ranges", len(ap.VersionRanges), 0)
}

func TestNVDParser_NonVulnerableCPESkipped(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-30008",
		"published": "2025-03-01T00:00:00.000",
		"lastModified": "2025-03-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Non-vulnerable CPE skip test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [
					{
						"vulnerable": false,
						"criteria": "cpe:2.3:o:vendor:os:*:*:*:*:*:*:*:*"
					},
					{
						"vulnerable": true,
						"criteria": "cpe:2.3:a:vendor:app:*:*:*:*:*:*:*:*",
						"versionEndExcluding": "2.0.0"
					}
				]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	// Only the vulnerable match should produce an affected package
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)
	if vulns[0].AffectedPackages[0].PackageName != "app" {
		t.Errorf("package: got %q, want app", vulns[0].AffectedPackages[0].PackageName)
	}
}

// ---------------------------------------------------------------------------
// CVEListV5Parser
// ---------------------------------------------------------------------------

func TestCVEListV5Parser_ValidRecord(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40001",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T12:00:00.000Z",
			"datePublished": "2025-04-05T08:00:00.000Z"
		},
		"containers": {
			"cna": {
				"title": "Remote code execution in acme-server",
				"descriptions": [
					{"lang": "en", "value": "A deserialization vulnerability in acme-server allows remote code execution via crafted JSON payloads."}
				],
				"affected": [{
					"vendor": "Acme Corp",
					"product": "acme-server",
					"packageName": "acme-server",
					"collectionURL": "https://www.npmjs.com/",
					"versions": [{
						"version": "1.0.0",
						"status": "affected",
						"lessThan": "1.5.0",
						"versionType": "semver"
					}]
				}],
				"references": [
					{"url": "https://acme.com/sa/2025-001", "tags": ["vendor-advisory"]},
					{"url": "https://github.com/acme/acme-server/commit/deadbeef", "tags": ["patch"]}
				],
				"metrics": [{
					"cvssV3_1": {
						"version": "3.1",
						"vectorString": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
						"baseScore": 9.8
					}
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	if v.ID != "CVE-2025-40001" {
		t.Errorf("ID: got %q", v.ID)
	}
	if v.Summary != "Remote code execution in acme-server" {
		t.Errorf("Summary: got %q", v.Summary)
	}
	if !strings.Contains(v.Details, "deserialization") {
		t.Errorf("Details should contain description, got %q", v.Details)
	}
	requireNonNil(t, "Published", v.Published)
	requireNonNil(t, "Modified", v.Modified)
	requireNil(t, "Withdrawn", v.Withdrawn)

	// Severity
	requireLen(t, "severity", len(v.Severity), 1)
	if v.Severity[0].Type != "CVSS_V3_1" {
		t.Errorf("severity type: got %q, want CVSS_V3_1", v.Severity[0].Type)
	}
	if v.Severity[0].Score != "9.8" {
		t.Errorf("severity score: got %q", v.Severity[0].Score)
	}

	// References
	requireLen(t, "references", len(v.References), 2)
	if v.References[0].Type != "ADVISORY" {
		t.Errorf("ref[0] type: got %q, want ADVISORY", v.References[0].Type)
	}
	if v.References[1].Type != "FIX" {
		t.Errorf("ref[1] type: got %q, want FIX", v.References[1].Type)
	}

	// Affected
	requireLen(t, "affected", len(v.AffectedPackages), 1)
	ap := v.AffectedPackages[0]
	if ap.PackageName != "acme-server" {
		t.Errorf("package: got %q", ap.PackageName)
	}
	if ap.Ecosystem != "npm" {
		t.Errorf("ecosystem: got %q, want npm", ap.Ecosystem)
	}
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].Introduced != "1.0.0" {
		t.Errorf("introduced: got %q", ap.VersionRanges[0].Introduced)
	}
	if ap.VersionRanges[0].Fixed != "1.5.0" {
		t.Errorf("fixed: got %q", ap.VersionRanges[0].Fixed)
	}
}

func TestCVEListV5Parser_RejectedState(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40002",
			"state": "REJECTED",
			"dateUpdated": "2025-04-15T10:00:00.000Z",
			"datePublished": "2025-04-01T08:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "This CVE was rejected."}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]
	if v.ID != "CVE-2025-40002" {
		t.Errorf("ID: got %q", v.ID)
	}
	requireNonNil(t, "Withdrawn", v.Withdrawn)
	requireNonNil(t, "Modified", v.Modified)
	if !v.Withdrawn.Equal(*v.Modified) {
		t.Errorf("Withdrawn should equal Modified for REJECTED state")
	}
	// Details should be empty for rejected
	if v.Details != "" {
		t.Errorf("Details should be empty for rejected, got %q", v.Details)
	}
}

func TestCVEListV5Parser_PackageNameResolution(t *testing.T) {
	tests := []struct {
		name     string
		affected string
		wantPkg  string
	}{
		{
			name:     "packageName takes priority",
			affected: `{"vendor": "Acme", "product": "widget", "packageName": "acme-widget"}`,
			wantPkg:  "acme-widget",
		},
		{
			name:     "product only when vendor present",
			affected: `{"vendor": "Acme", "product": "widget"}`,
			wantPkg:  "widget",
		},
		{
			name:     "product only",
			affected: `{"product": "standalone"}`,
			wantPkg:  "standalone",
		},
		{
			name:     "all empty",
			affected: `{}`,
			wantPkg:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(`{
				"dataType": "CVE_RECORD",
				"dataVersion": "5.1",
				"cveMetadata": {
					"cveId": "CVE-2025-40010",
					"state": "PUBLISHED",
					"dateUpdated": "2025-04-10T00:00:00.000Z"
				},
				"containers": {
					"cna": {
						"descriptions": [{"lang": "en", "value": "test"}],
						"affected": [` + tc.affected + `]
					}
				}
			}`)

			p := &parser.CVEListV5Parser{}
			vulns, err := p.Parse(data)
			requireNoError(t, err)
			requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)

			got := vulns[0].AffectedPackages[0].PackageName
			if got != tc.wantPkg {
				t.Errorf("PackageName: got %q, want %q", got, tc.wantPkg)
			}
		})
	}
}

func TestCVEListV5Parser_EcosystemFromURL(t *testing.T) {
	tests := []struct {
		url       string
		wantEco   string
	}{
		{"https://www.npmjs.com/", "npm"},
		{"https://pypi.org/project/", "PyPI"},
		{"https://crates.io/crates/", "crates.io"},
		{"https://pkg.go.dev/", "Go"},
		{"https://rubygems.org/gems/", "RubyGems"},
		{"https://repo1.maven.org/maven2/", "Maven"},
		{"https://www.nuget.org/packages/", "NuGet"},
		{"https://unknown.example.com/", ""},
	}

	for _, tc := range tests {
		t.Run(tc.wantEco, func(t *testing.T) {
			data := []byte(`{
				"dataType": "CVE_RECORD",
				"dataVersion": "5.1",
				"cveMetadata": {
					"cveId": "CVE-2025-40020",
					"state": "PUBLISHED",
					"dateUpdated": "2025-04-10T00:00:00.000Z"
				},
				"containers": {
					"cna": {
						"descriptions": [{"lang": "en", "value": "ecosystem test"}],
						"affected": [{
							"product": "test-pkg",
							"collectionURL": "` + tc.url + `",
							"versions": [{"version": "0", "status": "affected", "lessThan": "1.0.0"}]
						}]
					}
				}
			}`)

			p := &parser.CVEListV5Parser{}
			vulns, err := p.Parse(data)
			requireNoError(t, err)

			got := vulns[0].AffectedPackages[0].Ecosystem
			if got != tc.wantEco {
				t.Errorf("Ecosystem for %q: got %q, want %q", tc.url, got, tc.wantEco)
			}
		})
	}
}

func TestCVEListV5Parser_VersionLessThanOrEqual(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40030",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "lessThanOrEqual test"}],
				"affected": [{
					"product": "bounded-lib",
					"versions": [{
						"version": "2.0.0",
						"status": "affected",
						"lessThanOrEqual": "2.9.9",
						"versionType": "semver"
					}]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].LastAffected != "2.9.9" {
		t.Errorf("lastAffected: got %q, want 2.9.9", ap.VersionRanges[0].LastAffected)
	}
	if ap.VersionRanges[0].Fixed != "" {
		t.Errorf("fixed should be empty, got %q", ap.VersionRanges[0].Fixed)
	}
}

func TestCVEListV5Parser_DefaultStatusAffectedNoVersions(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40040",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "defaultStatus=affected with no versions"}],
				"affected": [{
					"product": "all-affected-lib",
					"defaultStatus": "affected"
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "unbounded_range") {
		t.Errorf("expected unbounded_range flag, got %v", ap.QualityFlags)
	}
	if !containsFlag(ap.QualityFlags, "empty_range") {
		t.Errorf("expected empty_range flag, got %v", ap.QualityFlags)
	}
}

func TestCVEListV5Parser_EmptyVersionsFlag(t *testing.T) {
	// No defaultStatus, but empty versions array -> empty_range but NOT unbounded_range
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40041",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "empty versions test"}],
				"affected": [{
					"product": "no-versions-lib"
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "empty_range") {
		t.Errorf("expected empty_range flag, got %v", ap.QualityFlags)
	}
	if containsFlag(ap.QualityFlags, "unbounded_range") {
		t.Errorf("should NOT have unbounded_range without defaultStatus=affected, got %v", ap.QualityFlags)
	}
}

func TestCVEListV5Parser_CVSSVersionHandling(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40050",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "CVSS version handling"}],
				"metrics": [
					{
						"cvssV3_1": {
							"version": "3.1",
							"vectorString": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:L/I:N/A:N",
							"baseScore": 5.3
						}
					},
					{
						"cvssV4_0": {
							"version": "4.0",
							"vectorString": "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:L/VI:N/VA:N/SC:N/SI:N/SA:N",
							"baseScore": 6.9
						}
					}
				]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "severity", len(vulns[0].Severity), 2)
	if vulns[0].Severity[0].Type != "CVSS_V3_1" {
		t.Errorf("severity[0] type: got %q, want CVSS_V3_1", vulns[0].Severity[0].Type)
	}
	if vulns[0].Severity[1].Type != "CVSS_V4_0" {
		t.Errorf("severity[1] type: got %q, want CVSS_V4_0", vulns[0].Severity[1].Type)
	}
}

func TestCVEListV5Parser_CVSSFallbackOrder(t *testing.T) {
	// When only cvssV3_0 is present (no v3_1 or v4_0), it should be used
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40051",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "CVSS fallback test"}],
				"metrics": [{
					"cvssV3_0": {
						"version": "3.0",
						"vectorString": "CVSS:3.0/AV:L/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N",
						"baseScore": 5.5
					}
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	requireLen(t, "severity", len(vulns[0].Severity), 1)
	if vulns[0].Severity[0].Type != "CVSS_V3_0" {
		t.Errorf("severity type: got %q, want CVSS_V3_0", vulns[0].Severity[0].Type)
	}
	if vulns[0].Severity[0].Score != "5.5" {
		t.Errorf("severity score: got %q, want 5.5", vulns[0].Severity[0].Score)
	}
}

func TestCVEListV5Parser_NoUpperBoundFlag(t *testing.T) {
	// affected version with no lessThan or lessThanOrEqual
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40060",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "no upper bound test"}],
				"affected": [{
					"product": "unbounded-v5",
					"versions": [{
						"version": "3.0.0",
						"status": "affected"
					}]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	if !containsFlag(ap.QualityFlags, "no_upper_bound") {
		t.Errorf("expected no_upper_bound flag, got %v", ap.QualityFlags)
	}
}

func TestCVEListV5Parser_UnaffectedVersionSkipped(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40070",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "unaffected skip test"}],
				"affected": [{
					"product": "mixed-status",
					"versions": [
						{"version": "1.0.0", "status": "affected", "lessThan": "1.5.0"},
						{"version": "2.0.0", "status": "unaffected"}
					]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 1)
	if ap.VersionRanges[0].Introduced != "1.0.0" {
		t.Errorf("introduced: got %q", ap.VersionRanges[0].Introduced)
	}
}

func TestCVEListV5Parser_TitleUsedAsSummary(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40080",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"title": "Buffer overflow in parser",
				"descriptions": [{"lang": "en", "value": "A very long description that would be truncated if used as summary."}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	if vulns[0].Summary != "Buffer overflow in parser" {
		t.Errorf("Summary should use title, got %q", vulns[0].Summary)
	}
	if vulns[0].Details != "A very long description that would be truncated if used as summary." {
		t.Errorf("Details should use description, got %q", vulns[0].Details)
	}
}

func TestCVEListV5Parser_SummaryTruncatedFromDetails(t *testing.T) {
	// No title, description is long -- Summary should be truncated from Details
	longDesc := strings.Repeat("A", 300)
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40081",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "` + longDesc + `"}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	// truncate(300, 256) -> 253 chars + "..."
	if len(vulns[0].Summary) != 256 {
		t.Errorf("Summary length: got %d, want 256", len(vulns[0].Summary))
	}
	if !strings.HasSuffix(vulns[0].Summary, "...") {
		t.Error("Summary should end with ...")
	}
}

func TestCVEListV5Parser_TruncateMultiByteUTF8(t *testing.T) {
	// Multi-byte characters: truncate should work on runes, not bytes
	// Each character is 3 bytes in UTF-8
	multiByteStr := strings.Repeat("世", 260) // 260 CJK chars
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40082",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "` + multiByteStr + `"}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	runes := []rune(vulns[0].Summary)
	if len(runes) != 256 {
		t.Errorf("Summary rune length: got %d, want 256", len(runes))
	}
}

func TestCVEListV5Parser_TruncateShortString(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40083",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "Short."}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	if vulns[0].Summary != "Short." {
		t.Errorf("Summary should not be truncated for short strings, got %q", vulns[0].Summary)
	}
}

func TestCVEListV5Parser_CPESInDatabaseSpecific(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-40090",
			"state": "PUBLISHED",
			"dateUpdated": "2025-04-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "CPE storage test"}],
				"affected": [{
					"product": "cpe-product",
					"cpes": ["cpe:2.3:a:vendor:cpe-product:*:*:*:*:*:*:*:*"]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)

	ap := vulns[0].AffectedPackages[0]
	dbSpec, ok := ap.DatabaseSpecific.(map[string]any)
	if !ok {
		t.Fatal("DatabaseSpecific should be map[string]any")
	}
	cpes, ok := dbSpec["cpes"]
	if !ok {
		t.Fatal("DatabaseSpecific should have cpes key")
	}
	cpeSlice, ok := cpes.([]string)
	if !ok {
		t.Fatalf("cpes should be []string, got %T", cpes)
	}
	if len(cpeSlice) != 1 {
		t.Errorf("cpes length: got %d, want 1", len(cpeSlice))
	}
}

// ---------------------------------------------------------------------------
// KEV batch
// ---------------------------------------------------------------------------

func TestParseKEVBatch_ValidData(t *testing.T) {
	data := []byte(`{
		"entries": [
			{
				"cveID": "CVE-2025-50001",
				"vendorProject": "Apache",
				"product": "HTTP Server",
				"dateAdded": "2025-03-01",
				"dueDate": "2025-03-22",
				"shortDescription": "Apache HTTP Server path traversal vulnerability",
				"requiredAction": "Apply updates per vendor instructions.",
				"notes": "https://httpd.apache.org/security/"
			},
			{
				"cveID": "CVE-2025-50002",
				"vendorProject": "Microsoft",
				"product": "Exchange Server",
				"dateAdded": "2025-04-10",
				"dueDate": "2025-05-01",
				"shortDescription": "Microsoft Exchange Server remote code execution vulnerability",
				"requiredAction": "Apply updates per vendor instructions.",
				"notes": ""
			}
		]
	}`)

	entries, err := parser.ParseKEVBatch(data)
	requireNoError(t, err)
	requireLen(t, "entries", len(entries), 2)

	e := entries[0]
	if e.CVEID != "CVE-2025-50001" {
		t.Errorf("CVEID: got %q", e.CVEID)
	}
	if e.VendorProject != "Apache" {
		t.Errorf("VendorProject: got %q", e.VendorProject)
	}
	if e.Product != "HTTP Server" {
		t.Errorf("Product: got %q", e.Product)
	}
	requireNonNil(t, "DateAdded", e.DateAdded)
	requireNonNil(t, "DueDate", e.DueDate)
	if e.ShortDescription == "" {
		t.Error("ShortDescription should not be empty")
	}
	if e.RequiredAction == "" {
		t.Error("RequiredAction should not be empty")
	}

	// Second entry
	if entries[1].CVEID != "CVE-2025-50002" {
		t.Errorf("entries[1] CVEID: got %q", entries[1].CVEID)
	}
}

func TestParseKEVBatch_EmptyEntries(t *testing.T) {
	data := []byte(`{"entries": []}`)

	entries, err := parser.ParseKEVBatch(data)
	requireNoError(t, err)
	requireLen(t, "entries", len(entries), 0)
}

func TestParseKEVBatch_MalformedJSON(t *testing.T) {
	data := []byte(`{"entries": [{"cveID": "CVE-2025-bad"`)

	_, err := parser.ParseKEVBatch(data)
	requireError(t, err)
	if !strings.Contains(err.Error(), "unmarshal kev batch") {
		t.Errorf("error should mention kev batch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EPSS batch
// ---------------------------------------------------------------------------

func TestParseEPSSBatch_ValidData(t *testing.T) {
	data := []byte(`{
		"model_version": "v2025.03.15",
		"score_date": "2025-03-20",
		"scores": [
			{"cve": "CVE-2025-60001", "epss": 0.00143, "percentile": 0.512},
			{"cve": "CVE-2025-60002", "epss": 0.97321, "percentile": 0.999}
		]
	}`)

	scores, scoreDate, err := parser.ParseEPSSBatch(data)
	requireNoError(t, err)
	requireLen(t, "scores", len(scores), 2)

	if scoreDate != "2025-03-20" {
		t.Errorf("scoreDate: got %q, want 2025-03-20", scoreDate)
	}

	s := scores[0]
	if s.CVEID != "CVE-2025-60001" {
		t.Errorf("CVEID: got %q", s.CVEID)
	}
	if s.Score < 0.001 || s.Score > 0.002 {
		t.Errorf("Score: got %f, want ~0.00143", s.Score)
	}
	if s.Percentile < 0.5 || s.Percentile > 0.52 {
		t.Errorf("Percentile: got %f, want ~0.512", s.Percentile)
	}
	if s.ModelVersion != "v2025.03.15" {
		t.Errorf("ModelVersion: got %q", s.ModelVersion)
	}
	if s.ScoreDate == nil {
		t.Error("ScoreDate: got nil")
	} else if s.ScoreDate.Format("2006-01-02") != "2025-03-20" {
		t.Errorf("ScoreDate: got %v, want 2025-03-20", s.ScoreDate)
	}

	// High-EPSS entry
	if scores[1].CVEID != "CVE-2025-60002" {
		t.Errorf("scores[1] CVEID: got %q", scores[1].CVEID)
	}
	if scores[1].Score < 0.97 {
		t.Errorf("scores[1] Score: got %f, want ~0.97321", scores[1].Score)
	}
}

func TestParseEPSSBatch_EmptyScores(t *testing.T) {
	data := []byte(`{
		"model_version": "v2025.03.15",
		"score_date": "2025-03-20",
		"scores": []
	}`)

	scores, scoreDate, err := parser.ParseEPSSBatch(data)
	requireNoError(t, err)
	requireLen(t, "scores", len(scores), 0)

	if scoreDate != "2025-03-20" {
		t.Errorf("scoreDate: got %q", scoreDate)
	}
}

func TestParseEPSSBatch_MalformedJSON(t *testing.T) {
	data := []byte(`{"scores": [{"cve":`)

	_, _, err := parser.ParseEPSSBatch(data)
	requireError(t, err)
	if !strings.Contains(err.Error(), "unmarshal epss batch") {
		t.Errorf("error should mention epss batch, got: %v", err)
	}
}

func TestParseEPSSBatch_ModelVersionExtraction(t *testing.T) {
	data := []byte(`{
		"model_version": "v2026.01.01",
		"score_date": "2026-01-05",
		"scores": [
			{"cve": "CVE-2026-00001", "epss": 0.5, "percentile": 0.75}
		]
	}`)

	scores, scoreDate, err := parser.ParseEPSSBatch(data)
	requireNoError(t, err)
	requireLen(t, "scores", len(scores), 1)

	if scoreDate != "2026-01-05" {
		t.Errorf("scoreDate: got %q, want 2026-01-05", scoreDate)
	}
	if scores[0].ModelVersion != "v2026.01.01" {
		t.Errorf("ModelVersion: got %q, want v2026.01.01", scores[0].ModelVersion)
	}
}

// ---------------------------------------------------------------------------
// OSVParser — additional coverage
// ---------------------------------------------------------------------------

func TestOSVParser_SeverityVectorDetection(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-sev-vec-0001",
		"modified": "2025-06-01T00:00:00Z",
		"severity": [
			{"type": "CVSS_V3", "score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N"},
			{"type": "CVSS_V3", "score": "7.5"}
		]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)
	requireLen(t, "severity", len(vulns[0].Severity), 2)

	// First entry: starts with "CVSS:" -> Vector field, Score empty
	sev0 := vulns[0].Severity[0]
	if sev0.Vector != "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:N/A:N" {
		t.Errorf("severity[0] Vector: got %q, want CVSS vector string", sev0.Vector)
	}
	if sev0.Score != "" {
		t.Errorf("severity[0] Score: got %q, want empty", sev0.Score)
	}

	// Second entry: numeric "7.5" -> Score field, Vector empty
	sev1 := vulns[0].Severity[1]
	if sev1.Score != "7.5" {
		t.Errorf("severity[1] Score: got %q, want %q", sev1.Score, "7.5")
	}
	if sev1.Vector != "" {
		t.Errorf("severity[1] Vector: got %q, want empty", sev1.Vector)
	}
}

func TestOSVParser_RangeType(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-rtype-0001",
		"modified": "2025-06-01T00:00:00Z",
		"affected": [
			{
				"package": {"ecosystem": "npm", "name": "semver-pkg"},
				"ranges": [{"type": "SEMVER", "events": [{"introduced": "1.0.0"}, {"fixed": "1.5.0"}]}]
			},
			{
				"package": {"ecosystem": "Go", "name": "git-pkg"},
				"ranges": [{"type": "GIT", "events": [{"introduced": "abc123"}, {"fixed": "def456"}]}]
			}
		]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 2)

	ap0 := vulns[0].AffectedPackages[0]
	requireLen(t, "ap0 version_ranges", len(ap0.VersionRanges), 1)
	if ap0.VersionRanges[0].RangeType != "SEMVER" {
		t.Errorf("ap0 RangeType: got %q, want SEMVER", ap0.VersionRanges[0].RangeType)
	}

	ap1 := vulns[0].AffectedPackages[1]
	requireLen(t, "ap1 version_ranges", len(ap1.VersionRanges), 1)
	if ap1.VersionRanges[0].RangeType != "GIT" {
		t.Errorf("ap1 RangeType: got %q, want GIT", ap1.VersionRanges[0].RangeType)
	}
}

func TestOSVParser_EcosystemSpecificMerge(t *testing.T) {
	data := []byte(`{
		"id": "GHSA-eco-merge-0001",
		"modified": "2025-06-01T00:00:00Z",
		"affected": [{
			"package": {"ecosystem": "PyPI", "name": "merge-pkg"},
			"ranges": [{"type": "ECOSYSTEM", "events": [{"introduced": "0"}, {"fixed": "2.0.0"}]}],
			"database_specific": {"severity": "HIGH", "source": "pypa"},
			"ecosystem_specific": {"imports": [{"module": "merge_pkg.core"}]}
		}]
	}`)

	p := &parser.OSVParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)

	ap := vulns[0].AffectedPackages[0]
	dbSpec, ok := ap.DatabaseSpecific.(map[string]any)
	if !ok {
		t.Fatalf("DatabaseSpecific should be map[string]any, got %T", ap.DatabaseSpecific)
	}

	// Original database_specific keys should be present
	if _, exists := dbSpec["severity"]; !exists {
		t.Error("merged map should contain 'severity' from database_specific")
	}
	if _, exists := dbSpec["source"]; !exists {
		t.Error("merged map should contain 'source' from database_specific")
	}

	// ecosystem_specific should be merged in
	if _, exists := dbSpec["ecosystem_specific"]; !exists {
		t.Error("merged map should contain 'ecosystem_specific' key")
	}
}

// ---------------------------------------------------------------------------
// NVDParser — additional coverage
// ---------------------------------------------------------------------------

func TestNVDParser_IntroducedExclusive(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-excl-0001",
		"published": "2025-06-01T00:00:00.000",
		"lastModified": "2025-06-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "IntroducedExclusive test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [
					{
						"vulnerable": true,
						"criteria": "cpe:2.3:a:vendor:excl_product:*:*:*:*:*:*:*:*",
						"versionStartExcluding": "1.0.0",
						"versionEndExcluding": "2.0.0"
					},
					{
						"vulnerable": true,
						"criteria": "cpe:2.3:a:vendor:incl_product:*:*:*:*:*:*:*:*",
						"versionStartIncluding": "3.0.0",
						"versionEndExcluding": "4.0.0"
					}
				]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 2)

	// versionStartExcluding -> IntroducedExclusive = true
	ap0 := vulns[0].AffectedPackages[0]
	requireLen(t, "ap0 version_ranges", len(ap0.VersionRanges), 1)
	if !ap0.VersionRanges[0].IntroducedExclusive {
		t.Error("versionStartExcluding should set IntroducedExclusive=true")
	}
	if ap0.VersionRanges[0].Introduced != "1.0.0" {
		t.Errorf("ap0 Introduced: got %q, want 1.0.0", ap0.VersionRanges[0].Introduced)
	}

	// versionStartIncluding -> IntroducedExclusive = false
	ap1 := vulns[0].AffectedPackages[1]
	requireLen(t, "ap1 version_ranges", len(ap1.VersionRanges), 1)
	if ap1.VersionRanges[0].IntroducedExclusive {
		t.Error("versionStartIncluding should leave IntroducedExclusive=false")
	}
	if ap1.VersionRanges[0].Introduced != "3.0.0" {
		t.Errorf("ap1 Introduced: got %q, want 3.0.0", ap1.VersionRanges[0].Introduced)
	}
}

func TestNVDParser_VendorExtraction(t *testing.T) {
	data := []byte(`{
		"id": "CVE-2025-vend-0001",
		"published": "2025-06-01T00:00:00.000",
		"lastModified": "2025-06-05T00:00:00.000",
		"vulnStatus": "Analyzed",
		"descriptions": [{"lang": "en", "value": "Vendor extraction test."}],
		"metrics": {},
		"configurations": [{
			"nodes": [{
				"operator": "OR",
				"negate": false,
				"cpeMatch": [{
					"vulnerable": true,
					"criteria": "cpe:2.3:a:apache:struts:*:*:*:*:*:*:*:*",
					"versionStartIncluding": "2.0.0",
					"versionEndExcluding": "2.5.33"
				}]
			}]
		}]
	}`)

	p := &parser.NVDParser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)

	ap := vulns[0].AffectedPackages[0]
	if ap.Vendor != "apache" {
		t.Errorf("Vendor: got %q, want %q", ap.Vendor, "apache")
	}
	if ap.PackageName != "struts" {
		t.Errorf("PackageName: got %q, want %q", ap.PackageName, "struts")
	}
	if ap.Ecosystem != "" {
		t.Errorf("Ecosystem should be empty for NVD records, got %q", ap.Ecosystem)
	}
}

// ---------------------------------------------------------------------------
// CVEListV5Parser — additional coverage
// ---------------------------------------------------------------------------

func TestCVEListV5Parser_DefaultStatusAffectedWithFixes(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-kernel-0001",
			"state": "PUBLISHED",
			"dateUpdated": "2025-06-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "kernel defaultStatus=affected pattern"}],
				"affected": [{
					"product": "linux_kernel",
					"vendor": "Linux",
					"defaultStatus": "affected",
					"versions": [
						{"version": "6.1", "status": "affected"},
						{"version": "0", "lessThan": "6.1", "status": "unaffected", "versionType": "semver"},
						{"version": "6.1.133", "lessThanOrEqual": "6.1.*", "status": "unaffected", "versionType": "semver"}
					]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	ap := vulns[0].AffectedPackages[0]
	if ap.PackageName != "linux_kernel" {
		t.Errorf("PackageName: got %q", ap.PackageName)
	}

	// The defaultStatus=affected logic should produce a range with
	// Introduced="6.1" and Fixed="6.1.133" (fix version from the
	// unaffected entry with lessThanOrEqual).
	found := false
	for _, vr := range ap.VersionRanges {
		if vr.Introduced == "6.1" && vr.Fixed == "6.1.133" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected VersionRange with Introduced=6.1, Fixed=6.1.133; got %+v", ap.VersionRanges)
	}
}

func TestCVEListV5Parser_ADPContainers(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-adp-0001",
			"state": "PUBLISHED",
			"dateUpdated": "2025-06-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "ADP container test"}],
				"affected": [{"product": "libfoo", "vendor": "acme"}]
			},
			"adp": [{
				"affected": [{"product": "widget", "vendor": "siemens"}],
				"metrics": [{
					"cvssV3_1": {
						"version": "3.1",
						"baseScore": 8.1,
						"vectorString": "CVSS:3.1/AV:N/AC:H/PR:N/UI:N/S:U/C:H/I:H/A:H"
					}
				}]
			}]
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "vulns", len(vulns), 1)

	v := vulns[0]

	// AffectedPackages should include entries from both CNA and ADP
	requireLen(t, "affected", len(v.AffectedPackages), 2)

	cnaAP := v.AffectedPackages[0]
	if cnaAP.PackageName != "libfoo" || cnaAP.Vendor != "acme" {
		t.Errorf("CNA affected: got pkg=%q vendor=%q", cnaAP.PackageName, cnaAP.Vendor)
	}

	adpAP := v.AffectedPackages[1]
	if adpAP.PackageName != "widget" || adpAP.Vendor != "siemens" {
		t.Errorf("ADP affected: got pkg=%q vendor=%q", adpAP.PackageName, adpAP.Vendor)
	}

	// Severity should include ADP metrics
	foundADPMetric := false
	for _, sev := range v.Severity {
		if sev.Score == "8.1" && strings.Contains(sev.Vector, "CVSS:3.1") {
			foundADPMetric = true
			break
		}
	}
	if !foundADPMetric {
		t.Errorf("ADP metrics not found in Severity; got %+v", v.Severity)
	}
}

func TestCVEListV5Parser_VendorSeparation(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-vsep-0001",
			"state": "PUBLISHED",
			"dateUpdated": "2025-06-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "vendor separation test"}],
				"affected": [{
					"vendor": "Red Hat",
					"product": "openshift",
					"versions": [{"version": "4.12", "status": "affected", "lessThan": "4.12.5"}]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)

	ap := vulns[0].AffectedPackages[0]
	if ap.Vendor != "Red Hat" {
		t.Errorf("Vendor: got %q, want %q", ap.Vendor, "Red Hat")
	}
	if ap.PackageName != "openshift" {
		t.Errorf("PackageName: got %q, want %q (vendor should not be concatenated)", ap.PackageName, "openshift")
	}
	// No collectionURL -> ecosystem should be empty
	if ap.Ecosystem != "" {
		t.Errorf("Ecosystem should be empty without collectionURL, got %q", ap.Ecosystem)
	}
}

func TestCVEListV5Parser_RangeType(t *testing.T) {
	data := []byte(`{
		"dataType": "CVE_RECORD",
		"dataVersion": "5.1",
		"cveMetadata": {
			"cveId": "CVE-2025-rtype-v5-0001",
			"state": "PUBLISHED",
			"dateUpdated": "2025-06-10T00:00:00.000Z"
		},
		"containers": {
			"cna": {
				"descriptions": [{"lang": "en", "value": "versionType stored as RangeType"}],
				"affected": [{
					"product": "range-type-pkg",
					"versions": [
						{"version": "1.0.0", "status": "affected", "lessThan": "2.0.0", "versionType": "semver"},
						{"version": "3.0.0", "status": "affected", "lessThan": "4.0.0", "versionType": "custom"}
					]
				}]
			}
		}
	}`)

	p := &parser.CVEListV5Parser{}
	vulns, err := p.Parse(data)
	requireNoError(t, err)
	requireLen(t, "affected", len(vulns[0].AffectedPackages), 1)

	ap := vulns[0].AffectedPackages[0]
	requireLen(t, "version_ranges", len(ap.VersionRanges), 2)

	if ap.VersionRanges[0].RangeType != "semver" {
		t.Errorf("VersionRanges[0].RangeType: got %q, want %q", ap.VersionRanges[0].RangeType, "semver")
	}
	if ap.VersionRanges[1].RangeType != "custom" {
		t.Errorf("VersionRanges[1].RangeType: got %q, want %q", ap.VersionRanges[1].RangeType, "custom")
	}
}
