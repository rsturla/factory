package source

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

// VendorNoteIndex is the subset of store needed for vendor note diffing.
type VendorNoteIndex interface {
	GetVendorNoteCVEIDs(ctx context.Context, vendor string) (map[string]time.Time, error)
}

type vendorNoteBatchEntry struct {
	CVEID   string          `json:"cve_id"`
	Content json.RawMessage `json:"content"`
}

const chunkSize = 1000

// writeChunkedBatches splits entries into chunks and writes each as a separate
// blob. Returns all blob keys. Each key becomes a separate resolver reconcile
// call with its own DB transaction.
func writeChunkedBatches(ctx context.Context, blobs blob.Store, vendor string, entries []vendorNoteBatchEntry, keyPrefix string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	ts := time.Now().UTC().Format("2006-01-02T150405")
	var keys []string

	for i := 0; i < len(entries); i += chunkSize {
		end := i + chunkSize
		if end > len(entries) {
			end = len(entries)
		}

		chunk := entries[i:end]
		key := fmt.Sprintf("%s/batch-%s-chunk-%03d.json", keyPrefix, ts, i/chunkSize)

		batch := map[string]any{"vendor": vendor, "notes": chunk}
		data, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("marshal chunk %d: %w", i/chunkSize, err)
		}
		if err := blobs.Put(ctx, key, data); err != nil {
			return nil, fmt.Errorf("put chunk %d: %w", i/chunkSize, err)
		}

		keys = append(keys, key)
	}

	return keys, nil
}
