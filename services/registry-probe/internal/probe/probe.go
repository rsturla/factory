package probe

import (
	"context"
	"time"
)

type PullResult struct {
	SizeBytes int64
	Layers    int
}

type PullFunc func(ctx context.Context, image string) (PullResult, error)

type Result struct {
	Image    string
	Success  bool
	Duration time.Duration
	Pull     PullResult
	Error    error
}

func Run(ctx context.Context, image string, pull PullFunc) Result {
	start := time.Now()
	pr, err := pull(ctx, image)
	return Result{
		Image:    image,
		Success:  err == nil,
		Duration: time.Since(start),
		Pull:     pr,
		Error:    err,
	}
}

type Metrics struct {
	OnSuccess func(result Result)
	OnFailure func(result Result)
}

func RunWithMetrics(ctx context.Context, image string, pull PullFunc, m Metrics) Result {
	result := Run(ctx, image, pull)
	if result.Success && m.OnSuccess != nil {
		m.OnSuccess(result)
	}
	if !result.Success && m.OnFailure != nil {
		m.OnFailure(result)
	}
	return result
}
