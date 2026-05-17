package blob

import (
	"context"
	"fmt"
	"os"
)

type Config struct {
	Backend  string
	LocalDir string
	S3       S3Config
}

func ConfigFromEnv() Config {
	return Config{
		Backend:  envOr("BLOB_BACKEND", "local"),
		LocalDir: envOr("BLOB_LOCAL_DIR", envOr("DATA_DIR", "/data")),
		S3: S3Config{
			Bucket:   os.Getenv("BLOB_BUCKET"),
			Endpoint: os.Getenv("BLOB_ENDPOINT"),
			Region:   envOr("BLOB_REGION", "us-east-1"),
			Prefix:   os.Getenv("BLOB_PREFIX"),
		},
	}
}

func New(ctx context.Context, cfg Config) (Store, error) {
	switch cfg.Backend {
	case "local", "":
		return NewLocalStore(cfg.LocalDir)
	case "s3":
		return NewS3Store(ctx, cfg.S3)
	default:
		return nil, fmt.Errorf("blob: unknown backend %q", cfg.Backend)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
