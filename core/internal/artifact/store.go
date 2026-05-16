// Package artifact provides storage for large stage outputs.
package artifact

import (
	"context"
	"fmt"
	"io"
)

// Store handles artifact upload/download.
// Implementations: S3, MinIO, local filesystem.
type Store interface {
	// Upload stores artifact data and returns URL.
	Upload(ctx context.Context, stageID string, data io.Reader) (string, error)

	// Download retrieves artifact data by URL.
	Download(ctx context.Context, url string) (io.ReadCloser, error)

	// Delete removes artifact by URL.
	Delete(ctx context.Context, url string) error

	// GetSize returns artifact size in bytes.
	GetSize(ctx context.Context, url string) (int64, error)
}

// Config for artifact storage.
type Config struct {
	Backend   string // "s3", "minio", "local"
	Endpoint  string // S3/MinIO endpoint
	Bucket    string // Bucket name
	Region    string // AWS region
	AccessKey string // Access key ID
	SecretKey string // Secret access key
	LocalDir  string // For local backend
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Backend:  "local",
		LocalDir: "/tmp/factory-artifacts",
	}
}

// NewStore creates artifact store from config.
func NewStore(ctx context.Context, cfg Config) (Store, error) {
	switch cfg.Backend {
	case "s3", "minio":
		return NewS3Store(ctx, cfg)
	case "local":
		localDir := cfg.LocalDir
		if localDir == "" {
			localDir = "/tmp/factory-artifacts"
		}
		return NewLocalStore(localDir)
	default:
		return nil, fmt.Errorf("unknown artifact backend: %s", cfg.Backend)
	}
}
