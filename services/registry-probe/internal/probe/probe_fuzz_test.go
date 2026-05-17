package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rsturla/factory/services/registry-probe/internal/probe"
)

func FuzzRun(f *testing.F) {
	f.Add("quay.io/hummingbird/core-runtime:latest", true)
	f.Add("", false)
	f.Add("invalid://not-a-ref!!!", true)
	f.Add("quay.io/a/b:c@sha256:"+string(make([]byte, 64)), false)
	f.Add("localhost:5000/image:tag", true)

	f.Fuzz(func(t *testing.T, image string, succeed bool) {
		pull := func(ctx context.Context, img string) (probe.PullResult, error) {
			if !succeed {
				return probe.PullResult{}, errors.New("fail")
			}
			return probe.PullResult{SizeBytes: 100, Layers: 1}, nil
		}

		result := probe.Run(context.Background(), image, pull)

		if result.Image != image {
			t.Fatalf("image mismatch: got %q, want %q", result.Image, image)
		}
		if result.Success != succeed {
			t.Fatalf("success mismatch: got %v, want %v", result.Success, succeed)
		}
		if result.Duration < 0 {
			t.Fatal("negative duration")
		}
	})
}
