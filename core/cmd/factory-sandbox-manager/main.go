package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/gitproxy"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/sandbox"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/postgres"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
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

	// Create sandbox provider from environment
	provider, err := sandbox.NewProviderFromEnv()
	if err != nil {
		logger.Error("create sandbox provider", "error", err)
		os.Exit(1)
	}

	providerType := os.Getenv("SANDBOX_PROVIDER")
	if providerType == "" {
		providerType = "docker"
	}
	logger.Info("sandbox provider initialized", "type", providerType)

	// Enqueue endpoint for sf-output queue
	enqueueEndpoint := os.Getenv("ENQUEUE_ENDPOINT")
	if enqueueEndpoint == "" {
		enqueueEndpoint = "http://localhost:8081"
	}

	// Git-proxy integration (optional)
	var tokenMinter *gitproxy.TokenMinter
	gitProxyURL := os.Getenv("GIT_PROXY_URL")
	if gitProxyURL != "" {
		secretHex := os.Getenv("TOKEN_SECRET")
		if secretHex == "" {
			logger.Warn("GIT_PROXY_URL set but TOKEN_SECRET missing, git access disabled")
		} else {
			secret, err := hex.DecodeString(secretHex)
			if err != nil {
				logger.Error("invalid TOKEN_SECRET format, must be hex-encoded", "error", err)
				os.Exit(1)
			}
			tokenMinter = gitproxy.NewTokenMinter(secret)
			logger.Info("git-proxy integration enabled", "proxy_url", gitProxyURL)
		}
	}

	rec := sandbox.NewReconciler(store, provider, enqueueEndpoint, tokenMinter, gitProxyURL, logger)

	// Serve reconciler HTTP endpoint
	handler := reconciler.ReconcilerHandler(rec.Reconcile)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8091"
	}

	server := &http.Server{
		Addr:    ":" + port,
		Handler: handler,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("starting factory-sandbox-manager", "port", port, "provider", providerType)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	logger.Info("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown error", "error", err)
	}
}
