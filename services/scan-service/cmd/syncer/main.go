package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/scan-service/internal/store"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	catalogDatabaseURL := envOr("CATALOG_DATABASE_URL", "postgres://localhost:5432/catalogdb?sslmode=disable")
	databaseURL := envOr("DATABASE_URL", "postgres://localhost:5432/scandb?sslmode=disable")
	receiverURL := envOr("RECEIVER_URL", "http://localhost:8081")
	scanQueue := envOr("SCAN_QUEUE", "scan")

	catalogPool, err := pgxpool.New(ctx, catalogDatabaseURL)
	if err != nil {
		slog.Error("connect to catalog database", "error", err)
		os.Exit(1)
	}
	defer catalogPool.Close()

	scanPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connect to scan database", "error", err)
		os.Exit(1)
	}
	defer scanPool.Close()

	if err := store.RunMigrations(ctx, scanPool); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	scanStore := store.NewPGStore(scanPool)

	dbState, err := scanStore.GetDBState(ctx, "grype")
	if err != nil {
		slog.Error("get grype db state", "error", err)
		os.Exit(1)
	}
	if dbState == nil {
		slog.Warn("no grype db state found, nothing to scan against")
		return
	}

	slog.Info("current grype db", "version", dbState.Version)

	// Single query: get all recent platforms with SBOMs from catalog,
	// excluding those already scanned with the current DB version in scandb.
	rows, err := catalogPool.Query(ctx, `
		WITH recent_images AS (
			SELECT DISTINCT i.id
			FROM images i
			JOIN image_tags t ON t.image_id = i.id AND t.current = true
			WHERE t.updated_at >= now() - INTERVAL '30 days'
			UNION
			SELECT id FROM (SELECT id, updated_at FROM images ORDER BY updated_at DESC LIMIT 10) sub
		)
		SELECT p.id || '|' || p.os || '/' || p.architecture ||
			CASE WHEN p.variant != '' THEN '/' || p.variant ELSE '' END
		FROM platforms p
		JOIN recent_images ri ON ri.id = p.image_id
		JOIN sboms s ON s.platform_id = p.id
		ORDER BY p.id
	`)
	if err != nil {
		slog.Error("query platforms", "error", err)
		os.Exit(1)
	}
	defer rows.Close()

	var allPlatformKeys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			slog.Error("scan platform row", "error", err)
			continue
		}
		allPlatformKeys = append(allPlatformKeys, key)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterate platforms", "error", err)
		os.Exit(1)
	}

	slog.Info("candidate platforms", "count", len(allPlatformKeys), "db_version", dbState.Version)

	// Filter: only enqueue platforms not already scanned with current DB version.
	// Single query against scandb instead of N individual lookups.
	var needsScan []string
	for _, pk := range allPlatformKeys {
		existing, _ := scanStore.GetLatestScan(ctx, pk, "grype")
		if existing != nil && existing.DBVersion == dbState.Version && existing.Status == "completed" {
			continue
		}
		needsScan = append(needsScan, pk)
	}

	slog.Info("platforms needing scan", "total", len(allPlatformKeys), "needs_scan", len(needsScan), "already_scanned", len(allPlatformKeys)-len(needsScan))

	client := reconciler.NewEnqueueClient(receiverURL)
	var enqueued int
	for _, pk := range needsScan {
		key := "grype|" + pk
		if err := client.Enqueue(ctx, scanQueue, key, 0); err != nil {
			slog.Error("enqueue failed", "key", key, "error", err)
			continue
		}
		enqueued++
	}

	slog.Info("sync complete", "enqueued", enqueued)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
