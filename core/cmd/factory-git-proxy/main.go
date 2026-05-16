package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/gitproxy"
	"gitlab.com/redhat/hummingbird/experimental/factory/core/internal/runstore/postgres"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Get configuration from env
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL required")
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8092"
	}

	// Token secret (32 bytes)
	secretHex := os.Getenv("TOKEN_SECRET")
	if secretHex == "" {
		logger.Warn("TOKEN_SECRET not set, generating random secret (not production safe)")
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("generate secret: %v", err)
		}
		secretHex = hex.EncodeToString(secret)
		logger.Info("generated secret", "secret", secretHex)
	}

	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		log.Fatalf("decode secret: %v", err)
	}

	// Git provider credentials
	// Format: GITHUB_TOKEN, GITLAB_TOKEN, etc.
	credentials := make(map[string]string)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		credentials["github.com"] = token
	}
	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		credentials["gitlab.com"] = token
	}

	// Connect to database
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	store := postgres.New(pool)

	// Create git-proxy server
	srv := gitproxy.NewServer(secret, store, credentials, logger)

	logger.Info("starting factory-git-proxy", "addr", listenAddr)
	if err := http.ListenAndServe(listenAddr, srv.Handler()); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
