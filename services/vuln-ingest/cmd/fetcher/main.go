package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/sdk/go/reconciler"

	"github.com/hummingbird-org/vuln-ingest/internal/fetch"
	"github.com/hummingbird-org/vuln-ingest/internal/fetch/source"
	"github.com/hummingbird-org/vuln-ingest/internal/store"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	listenAddr := envOr("LISTEN_ADDR", ":8082")
	databaseURL := envOr("DATABASE_URL", "postgres://localhost:5432/vulndb?sslmode=disable")
	dataDir := envOr("DATA_DIR", "/data")
	receiverURL := envOr("RECEIVER_URL", "http://localhost:8081")
	resolveQueue := envOr("RESOLVE_QUEUE", "vuln-resolve")

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := store.RunMigrations(ctx, pool); err != nil {
		slog.Error("run migrations", "error", err)
		os.Exit(1)
	}

	s := store.NewPGStore(pool)
	rec := fetch.NewReconciler(s, dataDir, receiverURL, resolveQueue)

	registerSources(rec, s)

	mux := http.NewServeMux()
	mux.Handle("POST /process", reconciler.ReconcilerHandler(rec.Reconcile))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		srv.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	slog.Info("fetcher starting", "addr", listenAddr, "data_dir", dataDir)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}

func registerSources(rec *fetch.Reconciler, s store.Store) {
	// Git-based sources: all use the shared GitSource implementation.
	gitSources := []struct {
		name, url, subDir, branch, glob string
	}{
		{"cvelistv5", "https://github.com/CVEProject/cvelistV5.git", "cves", "main", "*.json"},
		{"ghsa", "https://github.com/github/advisory-database.git", "advisories/github-reviewed", "main", "*.json"},
		{"rustsec", "https://github.com/rustsec/advisory-db.git", ".", "osv", "*.json"},
		{"govuln", "https://github.com/golang/vulndb.git", "data/osv", "master", "*.json"},
		{"pypa", "https://github.com/pypa/advisory-database.git", "vulns", "main", "*.yaml"},
		{"psf", "https://github.com/psf/advisory-database.git", "advisories", "main", "*.json"},
		{"kernel", "https://git.kernel.org/pub/scm/linux/security/vulns.git", "cve/published", "master", "*.json"},
		{"anchore-nvd-overrides", "https://github.com/anchore/nvd-data-overrides.git", "data", "main", "*.json"},
	}

	for _, gs := range gitSources {
		rec.RegisterSource(source.NewGitSource(gs.name, gs.url, gs.subDir, gs.branch, gs.glob))
	}

	// REST/download sources.
	rec.RegisterSource(source.NewNVDSource())
	rec.RegisterSource(source.NewOSVSource(defaultOSVEcosystems()))
	rec.RegisterSource(source.NewKEVSource(s))
	rec.RegisterSource(source.NewEPSSSource(s))
}

func defaultOSVEcosystems() []string {
	if v := os.Getenv("OSV_ECOSYSTEMS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"Linux", "OSS-Fuzz"}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
