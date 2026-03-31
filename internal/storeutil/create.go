// Package storeutil provides shared helpers for creating store.Interface
// instances from environment variables. Used by all cmd/ binaries.
package storeutil

import (
	"context"
	"fmt"
	"os"

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
		databaseURL := os.Getenv("DATABASE_URL")
		if databaseURL == "" {
			return nil, fmt.Errorf("DATABASE_URL required for postgres backend")
		}
		pool, err := pgxpool.New(ctx, databaseURL)
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
