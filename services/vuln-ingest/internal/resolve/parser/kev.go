package parser

import (
	"encoding/json"
	"fmt"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

type kevBatch struct {
	Entries []kevEntry `json:"entries"`
}

type kevEntry struct {
	CVEID            string `json:"cveID"`
	VendorProject    string `json:"vendorProject"`
	Product          string `json:"product"`
	DateAdded        string `json:"dateAdded"`
	DueDate          string `json:"dueDate"`
	ShortDescription string `json:"shortDescription"`
	RequiredAction   string `json:"requiredAction"`
	Notes            string `json:"notes"`
}

// ParseKEVBatch extracts KEV entries from a batch file.
func ParseKEVBatch(data []byte) ([]model.KEVEntry, error) {
	var batch kevBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, fmt.Errorf("unmarshal kev batch: %w", err)
	}

	entries := make([]model.KEVEntry, 0, len(batch.Entries))
	for _, e := range batch.Entries {
		entries = append(entries, model.KEVEntry{
			CVEID:            e.CVEID,
			VendorProject:    e.VendorProject,
			Product:          e.Product,
			DateAdded:        parseTime(e.DateAdded),
			DueDate:          parseTime(e.DueDate),
			ShortDescription: e.ShortDescription,
			RequiredAction:   e.RequiredAction,
			Notes:            e.Notes,
		})
	}

	return entries, nil
}
