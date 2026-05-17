package parser_test

import (
	"testing"

	"github.com/hummingbird-org/vuln-ingest/internal/resolve/parser"
)

func FuzzOSVParser(f *testing.F) {
	f.Add([]byte(`{"id":"GHSA-1234","aliases":["CVE-2024-0001"],"summary":"test","affected":[]}`))
	f.Add([]byte(`{"id":"RUSTSEC-2024-0001","affected":[{"package":{"ecosystem":"crates.io","name":"serde"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"},{"fixed":"1.0.0"}]}]}]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Add([]byte(`{"id":"","affected":[{"ranges":[{"events":[{"introduced":"0"}]}]}]}`))

	p := &parser.OSVParser{}
	f.Fuzz(func(t *testing.T, data []byte) {
		p.Parse(data) //nolint:errcheck
	})
}

func FuzzNVDParser(f *testing.F) {
	f.Add([]byte(`{"id":"CVE-2024-0001","published":"2024-01-01T00:00:00","lastModified":"2024-01-15T00:00:00","vulnStatus":"Analyzed","descriptions":[{"lang":"en","value":"test"}],"metrics":{},"configurations":[],"references":[]}`))
	f.Add([]byte(`{"id":"CVE-2024-0002","vulnStatus":"Rejected"}`))
	f.Add([]byte(`{"id":"CVE-2024-0003","metrics":{"cvssMetricV31":[{"source":"nvd","cvssData":{"version":"3.1","vectorString":"CVSS:3.1/AV:N/AC:L","baseScore":9.8}}]}}`))
	f.Add([]byte(`{"id":"CVE-2024-0004","metrics":{"cvssMetricV2":[{"source":"nvd","cvssV2":{"version":"2.0","vectorString":"AV:N/AC:L/Au:N/C:C/I:C/A:C","baseScore":10.0}}]}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"configurations":[{"nodes":[{"operator":"OR","cpeMatch":[{"vulnerable":true,"criteria":"cpe:2.3:a:vendor:product:*:*:*:*:*:*:*:*"}]}]}]}`))

	p := &parser.NVDParser{}
	f.Fuzz(func(t *testing.T, data []byte) {
		p.Parse(data) //nolint:errcheck
	})
}

func FuzzCVEListV5Parser(f *testing.F) {
	f.Add([]byte(`{"dataType":"CVE_RECORD","dataVersion":"5.0","cveMetadata":{"cveId":"CVE-2024-0001","state":"PUBLISHED","datePublished":"2024-01-01T00:00:00"},"containers":{"cna":{"title":"test","descriptions":[{"lang":"en","value":"test desc"}]}}}`))
	f.Add([]byte(`{"cveMetadata":{"cveId":"CVE-2024-0002","state":"REJECTED","dateUpdated":"2024-06-01T00:00:00"},"containers":{"cna":{}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"cveMetadata":{"cveId":"CVE-2024-0003"},"containers":{"cna":{"affected":[{"vendor":"apache","product":"httpd","versions":[{"version":"2.4.0","status":"affected","lessThan":"2.4.58"}]}]}}}`))

	p := &parser.CVEListV5Parser{}
	f.Fuzz(func(t *testing.T, data []byte) {
		p.Parse(data) //nolint:errcheck
	})
}

func FuzzParseKEVBatch(f *testing.F) {
	f.Add([]byte(`{"entries":[{"cveID":"CVE-2024-0001","vendorProject":"Apache","product":"httpd","dateAdded":"2024-01-01","dueDate":"2024-02-01","shortDescription":"test","requiredAction":"Apply update","notes":""}]}`))
	f.Add([]byte(`{"entries":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		parser.ParseKEVBatch(data) //nolint:errcheck
	})
}

func FuzzParseEPSSBatch(f *testing.F) {
	f.Add([]byte(`{"model_version":"v2024.01.01","score_date":"2024-01-15","scores":[{"cve":"CVE-2024-0001","epss":0.95,"percentile":0.99}]}`))
	f.Add([]byte(`{"scores":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, data []byte) {
		parser.ParseEPSSBatch(data) //nolint:errcheck
	})
}
