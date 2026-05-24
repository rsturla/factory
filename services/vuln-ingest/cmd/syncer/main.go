package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

var defaultSources = []string{
	"cvelistv5", "ghsa", "rustsec", "govuln", "pypa", "psf",
	"kernel", "anchore-nvd-overrides", "osv", "nvd", "kev", "epss",
	"vendor-notes-debian",
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	receiverURL := envOr("RECEIVER_URL", "http://localhost:8081")
	fetchQueue := envOr("FETCH_QUEUE", "vuln-fetch")
	sources := sourcesFromEnv()

	client := reconciler.NewEnqueueClient(receiverURL)

	for _, src := range sources {
		if err := client.Enqueue(ctx, fetchQueue, src, 0); err != nil {
			slog.Error("enqueue failed", "source", src, "error", err)
			os.Exit(1)
		}
		slog.Info("enqueued", "source", src, "queue", fetchQueue)
	}

	slog.Info("sync triggered", "sources", len(sources))
}

func sourcesFromEnv() []string {
	if v := os.Getenv("SOURCES"); v != "" {
		return strings.Split(v, ",")
	}
	return defaultSources
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
