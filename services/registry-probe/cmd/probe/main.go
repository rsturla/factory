package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rsturla/factory/services/registry-probe/internal/probe"
	imgcopy "go.podman.io/image/v5/copy"
	"go.podman.io/image/v5/docker"
	"go.podman.io/image/v5/manifest"
	"go.podman.io/image/v5/oci/layout"
	"go.podman.io/image/v5/signature"
	"go.podman.io/image/v5/types"
)

var (
	pullDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "registry_probe_pull_duration_seconds",
		Help:    "Time taken to pull the probe image.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60},
	}, []string{"image", "status"})

	pullTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "registry_probe_pull_total",
		Help: "Total number of pull attempts.",
	}, []string{"image", "status"})

	lastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "registry_probe_last_success_timestamp",
		Help: "Unix timestamp of the last successful pull.",
	}, []string{"image"})

	imageSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "registry_probe_image_size_bytes",
		Help: "Size of the pulled image in bytes.",
	}, []string{"image"})

	imageLayers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "registry_probe_layers_total",
		Help: "Number of layers in the pulled image.",
	}, []string{"image"})

	probeUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "registry_probe_up",
		Help: "Whether the last probe was successful (1) or not (0).",
	}, []string{"image"})
)

func init() {
	prometheus.MustRegister(pullDuration, pullTotal, lastSuccess, imageSize, imageLayers, probeUp)
}

func main() {
	listenAddr := envOr("LISTEN_ADDR", ":8080")
	image := envOr("PROBE_IMAGE", "quay.io/hummingbird/core-runtime:latest")
	interval := envOrDuration("PROBE_INTERVAL", 5*time.Minute)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		slog.Info("registry-probe starting", "addr", listenAddr, "image", image, "interval", interval)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	pull := func(ctx context.Context, img string) (probe.PullResult, error) {
		srcRef, err := docker.ParseReference("//" + img)
		if err != nil {
			return probe.PullResult{}, fmt.Errorf("parse reference: %w", err)
		}

		tmpDir, err := os.MkdirTemp("", "registry-probe-*")
		if err != nil {
			return probe.PullResult{}, fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		destRef, err := layout.ParseReference(tmpDir + ":" + "probe")
		if err != nil {
			return probe.PullResult{}, fmt.Errorf("parse dest: %w", err)
		}

		policy := &signature.Policy{
			Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()},
		}
		policyCtx, err := signature.NewPolicyContext(policy)
		if err != nil {
			return probe.PullResult{}, fmt.Errorf("policy context: %w", err)
		}
		defer policyCtx.Destroy()

		sysCtx := &types.SystemContext{}
		manifestBytes, err := imgcopy.Image(ctx, policyCtx, destRef, srcRef, &imgcopy.Options{
			SourceCtx: sysCtx,
		})
		if err != nil {
			return probe.PullResult{}, err
		}

		var pr probe.PullResult
		if ociManifest, err := manifest.OCI1FromManifest(manifestBytes); err == nil {
			pr.Layers = len(ociManifest.Layers)
			for _, l := range ociManifest.Layers {
				pr.SizeBytes += l.Size
			}
		} else if s2, err := manifest.Schema2FromManifest(manifestBytes); err == nil {
			pr.Layers = len(s2.LayersDescriptors)
			for _, l := range s2.LayersDescriptors {
				pr.SizeBytes += l.Size
			}
		}

		return pr, nil
	}

	metrics := probe.Metrics{
		OnSuccess: func(r probe.Result) {
			slog.Info("pull succeeded", "image", r.Image, "duration", r.Duration, "size_bytes", r.Pull.SizeBytes, "layers", r.Pull.Layers)
			lastSuccess.WithLabelValues(r.Image).SetToCurrentTime()
			pullDuration.WithLabelValues(r.Image, "success").Observe(r.Duration.Seconds())
			pullTotal.WithLabelValues(r.Image, "success").Inc()
			imageSize.WithLabelValues(r.Image).Set(float64(r.Pull.SizeBytes))
			imageLayers.WithLabelValues(r.Image).Set(float64(r.Pull.Layers))
			probeUp.WithLabelValues(r.Image).Set(1)
		},
		OnFailure: func(r probe.Result) {
			slog.Error("pull failed", "image", r.Image, "duration", r.Duration, "error", r.Error)
			pullDuration.WithLabelValues(r.Image, "failure").Observe(r.Duration.Seconds())
			pullTotal.WithLabelValues(r.Image, "failure").Inc()
			probeUp.WithLabelValues(r.Image).Set(0)
		},
	}

	var wg sync.WaitGroup

	runProbe := func() {
		wg.Add(1)
		defer wg.Done()
		probe.RunWithMetrics(ctx, image, pull, metrics)
	}

	runProbe()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			srv.Shutdown(context.Background())
			return
		case <-ticker.C:
			runProbe()
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			slog.Warn("invalid duration, using default", "key", key, "value", v, "default", fallback, "error", err)
			return fallback
		}
		return d
	}
	return fallback
}
