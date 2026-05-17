package discover

import (
	"testing"

	"github.com/rsturla/factory/services/catalog/internal/model"
)

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		ref          string
		wantRegistry string
		wantRepo     string
		wantTag      string
		wantErr      bool
	}{
		{
			ref:          "quay.io/hummingbird/core-runtime:latest",
			wantRegistry: "quay.io",
			wantRepo:     "hummingbird/core-runtime",
			wantTag:      "latest",
		},
		{
			ref:          "quay.io/hummingbird/go:1.26",
			wantRegistry: "quay.io",
			wantRepo:     "hummingbird/go",
			wantTag:      "1.26",
		},
		{
			ref:          "docker.io/library/nginx:1.25",
			wantRegistry: "docker.io",
			wantRepo:     "library/nginx",
			wantTag:      "1.25",
		},
		{
			ref:          "quay.io/hummingbird/core-runtime",
			wantRegistry: "quay.io",
			wantRepo:     "hummingbird/core-runtime",
			wantTag:      "",
		},
		{
			ref:          "quay.io/hummingbird/core-runtime@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			wantRegistry: "quay.io",
			wantRepo:     "hummingbird/core-runtime",
			wantTag:      "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			registry, repo, tag, err := parseImageRef(tt.ref)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseImageRef(%q) error = %v, wantErr %v", tt.ref, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if registry != tt.wantRegistry {
				t.Errorf("registry: got %q, want %q", registry, tt.wantRegistry)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo: got %q, want %q", repo, tt.wantRepo)
			}
			if tag != tt.wantTag {
				t.Errorf("tag: got %q, want %q", tag, tt.wantTag)
			}
		})
	}
}

func TestParsePlatformKey(t *testing.T) {
	tests := []struct {
		key        string
		wantDigest string
		wantArch   string
		wantErr    bool
	}{
		{
			key:        "sha256:abc123|linux/amd64",
			wantDigest: "sha256:abc123",
			wantArch:   "linux/amd64",
		},
		{
			key:        "sha256:def456|linux/arm64/v8",
			wantDigest: "sha256:def456",
			wantArch:   "linux/arm64/v8",
		},
		{
			key:     "invalid-no-pipe",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			digest, osArch, err := ParsePlatformKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePlatformKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if digest != tt.wantDigest {
				t.Errorf("digest: got %q, want %q", digest, tt.wantDigest)
			}
			if osArch != tt.wantArch {
				t.Errorf("osArch: got %q, want %q", osArch, tt.wantArch)
			}
		})
	}
}

func TestPlatformKey(t *testing.T) {
	tests := []struct {
		name string
		plat model.Platform
		want string
	}{
		{
			name: "amd64 no variant",
			plat: model.Platform{ID: "sha256:abc", OS: "linux", Architecture: "amd64"},
			want: "sha256:abc|linux/amd64",
		},
		{
			name: "arm64 with variant",
			plat: model.Platform{ID: "sha256:def", OS: "linux", Architecture: "arm64", Variant: "v8"},
			want: "sha256:def|linux/arm64/v8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := platformKey(tt.plat)
			if got != tt.want {
				t.Errorf("platformKey: got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSkipTag(t *testing.T) {
	skip := []string{
		"sha256-033c6e4a2663ab54df4af6e04cdd63127a1bdcf0716680a67a63b18c27ee7a4a",
		"sha256-033c6e4a.sig",
		"sha256-033c6e4a.att",
		"sha256-033c6e4a.sbom",
		"sha256-033c6e4a.src",
		"2-source",
		"2-builder-source",
		"2.42-openssl-fips-builder-source",
	}
	keep := []string{
		"latest",
		"2",
		"2.42",
		"2-builder",
		"2-openssl",
		"2-openssl-fips",
		"2.42-builder",
	}

	for _, tag := range skip {
		if !skipTag(tag) {
			t.Errorf("expected skip for %q", tag)
		}
	}
	for _, tag := range keep {
		if skipTag(tag) {
			t.Errorf("expected keep for %q", tag)
		}
	}
}
