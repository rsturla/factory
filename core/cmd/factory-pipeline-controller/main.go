package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/controller"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create controller
	ctrl := controller.NewPipelineController(logger)

	// Start controller
	go func() {
		if err := ctrl.Start(ctx); err != nil {
			logger.Error("controller failed", "error", err)
			os.Exit(1)
		}
	}()

	// Wait for shutdown signal
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	<-stop
	logger.Info("shutting down")
	cancel()
}
