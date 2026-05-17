package main

import (
	"context"
	"fmt"
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

	// Connect to catalog DB (read platforms with SBOMs)
	catalogPool, err := pgxpool.New(ctx, catalogDatabaseURL)
	if err != nil {
		slog.Error("connect to catalog database", "error", err)
		os.Exit(1)
	}
	defer catalogPool.Close()

	// Connect to scan DB (read existing scan state)
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

	// Get current Grype DB version from scan DB
	dbState, err := scanStore.GetDBState(ctx, "grype")
	if err != nil {
		slog.Error("get grype db state", "error", err)
		os.Exit(1)
	}

	currentDBVersion := ""
	if dbState != nil {
		currentDBVersion = dbState.Version
	}

	// Query catalog DB for platforms that have SBOMs
	// Filter: platforms from images with tags updated in last 30 days OR last 10 images
	rows, err := catalogPool.Query(ctx, `
		WITH recent_images AS (
			SELECT DISTINCT i.id
			FROM images i
			JOIN image_tags t ON t.image_id = i.id AND t.current = true
			WHERE t.updated_at >= now() - INTERVAL '30 days'
			UNION
			SELECT id FROM images ORDER BY updated_at DESC LIMIT 10
		)
		SELECT p.id, p.os, p.architecture
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

	type platformInfo struct {
		ID           string
		OS           string
		Architecture string
	}

	var platforms []platformInfo
	for rows.Next() {
		var p platformInfo
		if err := rows.Scan(&p.ID, &p.OS, &p.Architecture); err != nil {
			slog.Error("scan platform row", "error", err)
			os.Exit(1)
		}
		platforms = append(platforms, p)
	}
	if err := rows.Err(); err != nil {
		slog.Error("iterate platforms", "error", err)
		os.Exit(1)
	}

	slog.Info("discovered platforms with SBOMs", "count", len(platforms), "current_db_version", currentDBVersion)

	client := reconciler.NewEnqueueClient(receiverURL)
	enqueued := 0

	for _, p := range platforms {
		// Check if this platform has already been scanned with the current DB version
		if currentDBVersion != "" {
			latestScan, err := scanStore.GetLatestScan(ctx, p.ID+"|"+p.OS+"/"+p.Architecture, "grype")
			if err != nil {
				slog.Error("check latest scan", "error", err, "platform", p.ID)
				continue
			}
			if latestScan != nil && latestScan.DBVersion == currentDBVersion {
				continue
			}
		}

		key := fmt.Sprintf("grype|%s|%s/%s", p.ID, p.OS, p.Architecture)
		if err := client.Enqueue(ctx, scanQueue, key, 0); err != nil {
			slog.Error("enqueue failed", "key", key, "error", err)
			continue
		}
		enqueued++
		slog.Info("enqueued scan", "key", key, "queue", scanQueue)
	}

	slog.Info("sync complete", "platforms", len(platforms), "enqueued", enqueued)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
