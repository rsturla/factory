package parser

import (
	"encoding/json"
	"fmt"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

type vendorNoteBatch struct {
	Vendor string               `json:"vendor"`
	Notes  []vendorNoteBatchRaw `json:"notes"`
}

type vendorNoteBatchRaw struct {
	CVEID   string          `json:"cve_id"`
	Content json.RawMessage `json:"content"`
}

// ParseVendorNoteBatch extracts vendor notes from a batch file.
func ParseVendorNoteBatch(data []byte) ([]model.VendorNote, error) {
	var batch vendorNoteBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, fmt.Errorf("unmarshal vendor note batch: %w", err)
	}

	if batch.Vendor == "" {
		return nil, fmt.Errorf("vendor note batch missing vendor field")
	}

	notes := make([]model.VendorNote, 0, len(batch.Notes))
	for _, n := range batch.Notes {
		if n.CVEID == "" {
			continue
		}

		var content any
		if err := json.Unmarshal(n.Content, &content); err != nil {
			return nil, fmt.Errorf("unmarshal content for %s: %w", n.CVEID, err)
		}

		notes = append(notes, model.VendorNote{
			CVEID:   n.CVEID,
			Vendor:  batch.Vendor,
			Content: content,
		})
	}

	return notes, nil
}
