package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anchore/clio"
	"github.com/anchore/grype/grype/db/v6/distribution"
	"github.com/anchore/grype/grype/db/v6/installation"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rsturla/factory/services/scan-service/internal/store"
	"github.com/rsturla/factory/services/scan-service/internal/model"
	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"
)

var (
	dbVersion = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "scan_grypedb_update_timestamp",
		Help: "Unix timestamp of the last successful Grype DB update.",
	})
	dbUpdateTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "scan_grypedb_update_total",
		Help: "Total Grype DB update attempts.",
	}, []string{"status"})
)

func init() {
	prometheus.MustRegister(dbVersion, dbUpdateTotal)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	listenAddr := envOr("LISTEN_ADDR", ":8080")
	grypeDBDir := envOr("GRYPE_DB_DIR", "/data/grypedb")
	databaseURL := envOr("DATABASE_URL", "postgres://scandb:scandb@localhost:5432/scandb?sslmode=disable")
	catalogDatabaseURL := envOr("CATALOG_DATABASE_URL", "")
	receiverURL := envOr("RECEIVER_URL", "")
	scanQueue := envOr("SCAN_QUEUE", "scan")
	checkInterval := envOrDuration("CHECK_INTERVAL", 1*time.Hour)

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connect to scandb", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}
	s := store.NewPGStore(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", promhttp.Handler())

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx)
	}()

	go func() {
		slog.Info("db-updater starting", "addr", listenAddr, "db_dir", grypeDBDir, "interval", checkInterval)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	updateDB(ctx, s, grypeDBDir)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			updated := updateDB(ctx, s, grypeDBDir)
			if updated && receiverURL != "" && catalogDatabaseURL != "" {
				enqueueRescans(ctx, catalogDatabaseURL, receiverURL, scanQueue)
			}
		}
	}
}

func updateDB(ctx context.Context, s store.Store, dbDir string) bool {
	slog.Info("checking for Grype DB update")

	id := clio.Identification{Name: "scan-service-db-updater"}

	distCfg := distribution.DefaultConfig()
	distCfg.ID = id
	distCfg.RequireUpdateCheck = false

	instCfg := installation.DefaultConfig(id)
	instCfg.DBRootDir = dbDir
	instCfg.ValidateAge = false
	instCfg.ValidateChecksum = true

	client, err := distribution.NewClient(distCfg)
	if err != nil {
		slog.Error("create distribution client", "error", err)
		dbUpdateTotal.WithLabelValues("error").Inc()
		return false
	}

	curator, err := installation.NewCurator(instCfg, client)
	if err != nil {
		slog.Error("create curator", "error", err)
		dbUpdateTotal.WithLabelValues("error").Inc()
		return false
	}

	updated, err := curator.Update()
	if err != nil {
		slog.Error("update grype db", "error", err)
		dbUpdateTotal.WithLabelValues("error").Inc()
		return false
	}

	status := curator.Status()
	slog.Info("grype db status", "version", status.SchemaVersion, "built", status.Built, "updated", updated)

	dbVersionStr := status.Built.String()
	if err := s.UpsertDBState(ctx, model.ScannerDBState{
		Scanner:   "grype",
		Version:   dbVersionStr,
		Checksum:  status.SchemaVersion,
		UpdatedAt: time.Now(),
	}); err != nil {
		slog.Error("persist db state", "error", err)
	}

	dbVersion.SetToCurrentTime()

	if updated {
		dbUpdateTotal.WithLabelValues("updated").Inc()
		slog.Info("grype db updated", "version", status.SchemaVersion)
	} else {
		dbUpdateTotal.WithLabelValues("current").Inc()
	}

	return updated
}

func enqueueRescans(ctx context.Context, catalogDBURL, receiverURL, scanQueue string) {
	slog.Info("enqueuing rescans after DB update")

	catalogPool, err := pgxpool.New(ctx, catalogDBURL)
	if err != nil {
		slog.Error("connect to catalogdb for rescan", "error", err)
		return
	}
	defer catalogPool.Close()

	rows, err := catalogPool.Query(ctx, `
		SELECT DISTINCT p.id || '|' || p.os || '/' || p.architecture ||
			CASE WHEN p.variant != '' THEN '/' || p.variant ELSE '' END
		FROM platforms p
		JOIN image_tags it ON it.image_id = p.image_id AND it.current = true
		WHERE it.updated_at > now() - INTERVAL '30 days'
		LIMIT 1000
	`)
	if err != nil {
		slog.Error("query platforms for rescan", "error", err)
		return
	}
	defer rows.Close()

	client := reconciler.NewEnqueueClient(receiverURL)
	var enqueued int
	for rows.Next() {
		var platformKey string
		if err := rows.Scan(&platformKey); err != nil {
			slog.Error("scan platform key", "error", err)
			continue
		}
		key := "grype|" + platformKey
		if err := client.Enqueue(ctx, scanQueue, key, 0); err != nil {
			slog.Error("enqueue rescan", "key", key, "error", err)
			continue
		}
		enqueued++
	}

	slog.Info("rescans enqueued", "count", enqueued)
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
			slog.Warn("invalid duration, using default", "key", key, "value", v, "default", fallback)
			return fallback
		}
		return d
	}
	return fallback
}
