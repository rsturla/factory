package scan

import "testing"

func FuzzParseKey(f *testing.F) {
	f.Add("grype|sha256:abc|linux/amd64")
	f.Add("|sha256:abc|linux/amd64")
	f.Add("grype||")
	f.Add("grype|sha256:abc|linux/amd64/v8")
	f.Add("")
	f.Add("no-pipe-at-all")
	f.Add("grype|")
	f.Add("|")
	f.Add("||")
	f.Add("|||")
	f.Add("scanner|sha256:e3b0c44298fc|linux/arm64/v8")

	f.Fuzz(func(t *testing.T, key string) {
		// parseKey should not panic on any input
		_, _, _ = parseKey(key)
	})
}

func FuzzParsePlatformKey(f *testing.F) {
	f.Add("sha256:abc|linux/amd64")
	f.Add("sha256:abc|linux/arm64/v8")
	f.Add("|linux/amd64")
	f.Add("sha256:abc|")
	f.Add("")
	f.Add("no-pipe-at-all")

	f.Fuzz(func(t *testing.T, key string) {
		// parsePlatformKey should not panic on any input
		_, _, _ = parsePlatformKey(key)
	})
}

func FuzzSbomBlobKey(f *testing.F) {
	f.Add("sha256:abc123")
	f.Add("sha256:")
	f.Add("abc123")
	f.Add("")
	f.Add("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	f.Fuzz(func(t *testing.T, digest string) {
		// sbomBlobKey should not panic on any input
		result := sbomBlobKey(digest)
		if result == "" {
			t.Error("sbomBlobKey returned empty string")
		}
	})
}
