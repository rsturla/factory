// Package storeutil provides shared helpers for creating store.Interface
// instances from environment variables. Used by all cmd/ binaries.
package storeutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/factory-workqueue/internal/store"
	storeddb "github.com/hummingbird-org/factory-workqueue/internal/store/dynamodb"
	storepostgres "github.com/hummingbird-org/factory-workqueue/internal/store/postgres"
	storesqlite "github.com/hummingbird-org/factory-workqueue/internal/store/sqlite"
)

// Result holds the created store and any resources that need cleanup.
type Result struct {
	Store store.Interface
	// Pool is non-nil for postgres backend; caller should defer Pool.Close().
	Pool *pgxpool.Pool
}

// CreateFromEnv creates a store.Interface based on STORE_BACKEND env var.
// Supported backends: "postgres" (default), "dynamodb", "sqlite".
func CreateFromEnv(ctx context.Context) (*Result, error) {
	backend := os.Getenv("STORE_BACKEND")
	if backend == "" {
		backend = "postgres"
	}

	switch backend {
	case "postgres":
		databaseURL := os.Getenv("PG_DATABASE_URL")
		if databaseURL == "" {
			return nil, fmt.Errorf("PG_DATABASE_URL required for postgres backend")
		}
		cfg, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			return nil, fmt.Errorf("parse database URL: %w", err)
		}
		cfg.MaxConns = 20
		cfg.MinConns = 2
		cfg.MaxConnLifetime = 30 * time.Minute
		cfg.MaxConnIdleTime = 5 * time.Minute
		cfg.HealthCheckPeriod = 30 * time.Second

		if v := os.Getenv("PG_MAX_CONNS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.MaxConns = int32(n)
			}
		}
		if v := os.Getenv("PG_MIN_CONNS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.MinConns = int32(n)
			}
		}
		if v := os.Getenv("PG_MAX_CONN_LIFETIME"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				cfg.MaxConnLifetime = d
			}
		}
		if v := os.Getenv("PG_HEALTH_CHECK_PERIOD"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				cfg.HealthCheckPeriod = d
			}
		}

		slog.Info("postgres pool configured",
			"max_conns", cfg.MaxConns,
			"min_conns", cfg.MinConns,
			"max_conn_lifetime", cfg.MaxConnLifetime,
			"health_check_period", cfg.HealthCheckPeriod,
		)

		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("connect to postgres: %w", err)
		}
		pgStore := storepostgres.New(pool)
		if err := pgStore.Migrate(ctx); err != nil {
			pool.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
		return &Result{Store: pgStore, Pool: pool}, nil

	case "dynamodb":
		table := os.Getenv("DDB_TABLE")
		if table == "" {
			return nil, fmt.Errorf("DDB_TABLE required for dynamodb backend")
		}
		bucket := os.Getenv("S3_BUCKET")
		if bucket == "" {
			return nil, fmt.Errorf("S3_BUCKET required for dynamodb backend")
		}
		ddbStore, err := storeddb.New(ctx, storeddb.Config{
			TableName:     table,
			HistoryBucket: bucket,
			Region:        os.Getenv("AWS_REGION"),
			DDBEndpoint:   os.Getenv("DDB_ENDPOINT"),
			S3Endpoint:    os.Getenv("S3_ENDPOINT"),
		})
		if err != nil {
			return nil, fmt.Errorf("create dynamodb store: %w", err)
		}
		if err := ddbStore.CreateTable(ctx); err != nil {
			return nil, fmt.Errorf("create dynamodb table: %w", err)
		}
		return &Result{Store: ddbStore}, nil

	case "sqlite":
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			return nil, fmt.Errorf("SQLITE_PATH required for sqlite backend")
		}
		sqliteStore, err := storesqlite.New(path)
		if err != nil {
			return nil, fmt.Errorf("create sqlite store: %w", err)
		}
		return &Result{Store: sqliteStore}, nil

	default:
		return nil, fmt.Errorf("unsupported store backend: %q", backend)
	}
}
