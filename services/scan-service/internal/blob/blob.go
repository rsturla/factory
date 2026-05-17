package blob

import (
	"context"
	"errors"
)

var ErrNotFound = errors.New("blob: not found")

// Store abstracts read/write access to advisory file blobs.
// Keys are slash-separated paths like "sboms/abc123.spdx.json".
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Exists(ctx context.Context, key string) (bool, error)
}
