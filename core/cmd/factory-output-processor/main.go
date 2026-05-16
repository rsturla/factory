package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/output"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Connect to PostgreSQL
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://localhost/factory?sslmode=disable"
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		logger.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := postgres.New(pool)

	// Create output processor
	proc := output.NewReconciler(store, logger)

	// Wrap in reconciler handler
	handler := reconciler.ReconcilerHandler(proc.Reconcile)

	// Listen for dispatcher requests
	port := os.Getenv("PORT")
	if port == "" {
		port = "8093"
	}

	logger.Info("starting factory-output-processor", "port", port)
	if err := http.ListenAndServe(":"+port, handler); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
