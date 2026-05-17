package source

import (
	"context"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

// Source fetches changes from an upstream vulnerability feed.
// Adding a new data source = implement this interface + register in config.
type Source interface {
	Name() string
	Fetch(ctx context.Context, blobs blob.Store, checkpoint string) (FetchResult, error)
}

type FetchResult struct {
	// ChangedFiles are relative paths on the shared volume, used as keys in the resolve queue.
	ChangedFiles []string

	// NewCheckpoint is persisted for the next diff-based sync.
	NewCheckpoint string

	// ItemCount is the number of changed items (for metrics/logging).
	ItemCount int
}
