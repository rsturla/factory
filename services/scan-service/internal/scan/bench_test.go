package scan

import (
	"context"
	"fmt"
	"testing"
)

type benchScanner struct{}

func (b *benchScanner) Name() string { return "bench" }
func (b *benchScanner) Scan(_ context.Context, sbomBytes []byte) ([]Finding, ScanMeta, error) {
	findings := make([]Finding, 50)
	for i := range findings {
		findings[i] = Finding{
			VulnID:         fmt.Sprintf("CVE-2024-%04d", i),
			Severity:       "Medium",
			PackageName:    fmt.Sprintf("pkg-%d", i),
			PackageVersion: "1.0.0",
			PackageType:    "rpm",
		}
	}
	return findings, ScanMeta{DBVersion: "bench-v1"}, nil
}

type benchStore struct{}

func (s *benchStore) UpsertScan(_ context.Context, _ interface{ any }) error { return nil }

type benchBlobStore struct {
	data []byte
}

func (b *benchBlobStore) Put(_ context.Context, _ string, _ []byte) error        { return nil }
func (b *benchBlobStore) Get(_ context.Context, _ string) ([]byte, error)        { return b.data, nil }
func (b *benchBlobStore) Exists(_ context.Context, _ string) (bool, error)       { return true, nil }

func BenchmarkParseKey(b *testing.B) {
	key := "grype|sha256:abcdef1234567890|linux/amd64"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = parseKey(key)
	}
}

func BenchmarkSbomBlobKey(b *testing.B) {
	digest := "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sbomBlobKey(digest)
	}
}

func BenchmarkCountSeverities(b *testing.B) {
	findings := make([]Finding, 100)
	for i := range findings {
		switch i % 4 {
		case 0:
			findings[i].Severity = "Critical"
		case 1:
			findings[i].Severity = "High"
		case 2:
			findings[i].Severity = "Medium"
		case 3:
			findings[i].Severity = "Low"
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var c, h, m, l int
		for _, f := range findings {
			switch f.Severity {
			case "Critical":
				c++
			case "High":
				h++
			case "Medium":
				m++
			case "Low":
				l++
			}
		}
		_ = c + h + m + l
	}
}
