package probe_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rsturla/factory/services/registry-probe/internal/probe"
)

func TestRun_Success(t *testing.T) {
	pull := func(ctx context.Context, image string) (probe.PullResult, error) {
		return probe.PullResult{SizeBytes: 13_000_000, Layers: 3}, nil
	}

	result := probe.Run(context.Background(), "quay.io/test/image:latest", pull)

	if !result.Success {
		t.Fatal("expected success")
	}
	if result.Error != nil {
		t.Fatalf("expected no error, got %v", result.Error)
	}
	if result.Image != "quay.io/test/image:latest" {
		t.Fatalf("expected image ref, got %q", result.Image)
	}
	if result.Duration <= 0 {
		t.Fatal("expected positive duration")
	}
	if result.Pull.SizeBytes != 13_000_000 {
		t.Fatalf("expected 13MB, got %d", result.Pull.SizeBytes)
	}
	if result.Pull.Layers != 3 {
		t.Fatalf("expected 3 layers, got %d", result.Pull.Layers)
	}
}

func TestRun_Failure(t *testing.T) {
	pull := func(ctx context.Context, image string) (probe.PullResult, error) {
		return probe.PullResult{}, errors.New("registry unavailable")
	}

	result := probe.Run(context.Background(), "quay.io/test/image:latest", pull)

	if result.Success {
		t.Fatal("expected failure")
	}
	if result.Error == nil {
		t.Fatal("expected error")
	}
	if result.Error.Error() != "registry unavailable" {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	pull := func(ctx context.Context, image string) (probe.PullResult, error) {
		return probe.PullResult{}, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := probe.Run(ctx, "quay.io/test/image:latest", pull)

	if result.Success {
		t.Fatal("expected failure on cancelled context")
	}
	if !errors.Is(result.Error, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", result.Error)
	}
}

func TestRun_MeasuresDuration(t *testing.T) {
	pull := func(ctx context.Context, image string) (probe.PullResult, error) {
		time.Sleep(10 * time.Millisecond)
		return probe.PullResult{}, nil
	}

	result := probe.Run(context.Background(), "quay.io/test/image:latest", pull)

	if result.Duration < 10*time.Millisecond {
		t.Fatalf("expected duration >= 10ms, got %v", result.Duration)
	}
}

func TestRunWithMetrics_Success(t *testing.T) {
	var called bool
	m := probe.Metrics{
		OnSuccess: func(r probe.Result) {
			called = true
			if r.Image != "quay.io/test:latest" {
				t.Fatalf("unexpected image: %q", r.Image)
			}
			if r.Pull.SizeBytes != 5000 {
				t.Fatalf("unexpected size: %d", r.Pull.SizeBytes)
			}
		},
		OnFailure: func(r probe.Result) {
			t.Fatal("OnFailure should not be called")
		},
	}

	probe.RunWithMetrics(context.Background(), "quay.io/test:latest", func(ctx context.Context, img string) (probe.PullResult, error) {
		return probe.PullResult{SizeBytes: 5000, Layers: 1}, nil
	}, m)

	if !called {
		t.Fatal("OnSuccess not called")
	}
}

func TestRunWithMetrics_Failure(t *testing.T) {
	var called bool
	m := probe.Metrics{
		OnSuccess: func(r probe.Result) {
			t.Fatal("OnSuccess should not be called")
		},
		OnFailure: func(r probe.Result) {
			called = true
			if r.Error.Error() != "broken" {
				t.Fatalf("unexpected error: %v", r.Error)
			}
		},
	}

	probe.RunWithMetrics(context.Background(), "quay.io/test:latest", func(ctx context.Context, img string) (probe.PullResult, error) {
		return probe.PullResult{}, errors.New("broken")
	}, m)

	if !called {
		t.Fatal("OnFailure not called")
	}
}

func TestRunWithMetrics_NilCallbacks(t *testing.T) {
	m := probe.Metrics{}
	result := probe.RunWithMetrics(context.Background(), "quay.io/test:latest", func(ctx context.Context, img string) (probe.PullResult, error) {
		return probe.PullResult{SizeBytes: 100, Layers: 2}, nil
	}, m)

	if !result.Success {
		t.Fatal("expected success")
	}
}
