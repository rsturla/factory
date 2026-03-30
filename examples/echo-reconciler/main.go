// echo-reconciler is a minimal test reconciler that logs each key it processes
// and returns "completed". Use it to verify the factory stack end-to-end.
//
// Usage:
//
//	go run ./examples/echo-reconciler/
//	curl -X POST http://localhost:8082/process -d '{"key":"hello","attempt":1}'
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/hummingbird-org/factory/pkg/sdk"
)

func main() {
	addr := envOr("LISTEN_ADDR", ":8082")
	delay := envOr("PROCESS_DELAY", "1s")
	processDuration, _ := time.ParseDuration(delay)

	mux := http.NewServeMux()
	mux.Handle("POST /process", sdk.ReconcilerHandler(func(ctx context.Context, req sdk.ProcessRequest) (sdk.ProcessResponse, error) {
		slog.Info("processing",
			"key", req.Key,
			"attempt", req.Attempt,
			"priority", req.Priority,
		)

		// Simulate work.
		time.Sleep(processDuration)

		slog.Info("completed", "key", req.Key)
		return sdk.Completed(), nil
	}))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	slog.Info("echo-reconciler starting", "addr", addr, "delay", delay)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
