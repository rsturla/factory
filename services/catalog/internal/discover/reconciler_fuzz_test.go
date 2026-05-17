package discover

import (
	"testing"
)

func FuzzParseImageRef(f *testing.F) {
	f.Add("quay.io/hummingbird/core-runtime:latest")
	f.Add("docker.io/library/nginx")
	f.Add("quay.io/a/b@sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")
	f.Add("localhost:5000/myimage:v1")
	f.Add("")

	f.Fuzz(func(t *testing.T, ref string) {
		registry, repo, tag, err := parseImageRef(ref)
		if err != nil {
			return
		}
		if registry == "" {
			t.Error("registry should not be empty on success")
		}
		if repo == "" {
			t.Error("repo should not be empty on success")
		}
		_ = tag
	})
}

func FuzzParsePlatformKey(f *testing.F) {
	f.Add("sha256:abc|linux/amd64")
	f.Add("sha256:def|linux/arm64/v8")
	f.Add("invalid")
	f.Add("|")
	f.Add("a|b|c")

	f.Fuzz(func(t *testing.T, key string) {
		_, _, _ = ParsePlatformKey(key)
	})
}
