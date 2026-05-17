package blob

import (
	"context"
	"testing"
)

func TestS3Store_fullKey(t *testing.T) {
	tests := []struct {
		prefix string
		key    string
		want   string
	}{
		{"", "nvd/CVE-2024-0001.json", "nvd/CVE-2024-0001.json"},
		{"vuln-ingest", "nvd/CVE-2024-0001.json", "vuln-ingest/nvd/CVE-2024-0001.json"},
		{"prefix/", "key.json", "prefix/key.json"},
	}

	for _, tt := range tests {
		s := &S3Store{prefix: trimSuffix(tt.prefix, "/")}
		got := s.fullKey(tt.key)
		if got != tt.want {
			t.Errorf("fullKey(%q) with prefix %q: got %q, want %q", tt.key, tt.prefix, got, tt.want)
		}
	}
}

func TestS3Store_validateKey(t *testing.T) {
	s := &S3Store{}

	valid := []string{
		"nvd/CVE-2024-0001.json",
		"osv/linux/GHSA-1234.json",
		"key.json",
		"a/b/c/deep.json",
	}
	for _, key := range valid {
		if err := s.validateKey(key); err != nil {
			t.Errorf("validateKey(%q) unexpected error: %v", key, err)
		}
	}

	invalid := []struct {
		key  string
		desc string
	}{
		{"", "empty key"},
		{"../../etc/passwd", "path traversal"},
		{"foo/../bar", "path traversal mid-key"},
		{"/absolute/path", "absolute path"},
		{"foo\x00bar", "null byte"},
	}
	for _, tt := range invalid {
		if err := s.validateKey(tt.key); err == nil {
			t.Errorf("validateKey(%q) [%s]: expected error", tt.key, tt.desc)
		}
	}
}

func TestNewS3Store_EmptyBucket(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3Config{Region: "us-east-1"})
	if err == nil {
		t.Fatal("expected error for empty bucket")
	}
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}
