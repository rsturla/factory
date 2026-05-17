package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// KEVIndex is the subset of store needed for KEV diffing.
type KEVIndex interface {
	GetAllKEVIDs(ctx context.Context) (map[string]time.Time, error)
}

// KEVSource downloads the CISA Known Exploited Vulnerabilities catalog,
// diffs against DB, and writes a batch file of changes.
type KEVSource struct {
	index      KEVIndex
	httpClient *http.Client
}

func NewKEVSource(idx KEVIndex) *KEVSource {
	return &KEVSource{
		index:      idx,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

func (k *KEVSource) Name() string { return "kev" }

type kevCatalog struct {
	Title           string       `json:"title"`
	CatalogVersion  string       `json:"catalogVersion"`
	DateReleased    string       `json:"dateReleased"`
	Count           int          `json:"count"`
	Vulnerabilities []kevVulnRaw `json:"vulnerabilities"`
}

type kevVulnRaw struct {
	CVEID            string `json:"cveID"`
	VendorProject    string `json:"vendorProject"`
	Product          string `json:"product"`
	VulnName         string `json:"vulnerabilityName"`
	DateAdded        string `json:"dateAdded"`
	ShortDescription string `json:"shortDescription"`
	RequiredAction   string `json:"requiredAction"`
	DueDate          string `json:"dueDate"`
	Notes            string `json:"notes"`
}

func (k *KEVSource) Fetch(ctx context.Context, dataDir string, checkpoint string) (FetchResult, error) {
	log := slog.With("source", "kev")

	catalog, err := k.download(ctx)
	if err != nil {
		return FetchResult{}, err
	}

	if catalog.CatalogVersion == checkpoint {
		log.Info("kev catalog unchanged", "version", checkpoint)
		return FetchResult{NewCheckpoint: checkpoint}, nil
	}

	existingIDs, err := k.index.GetAllKEVIDs(ctx)
	if err != nil {
		return FetchResult{}, fmt.Errorf("get existing kev ids: %w", err)
	}

	// Diff: catalog version changed, so include all entries not yet in DB
	// plus re-enqueue all existing ones (their fields may have been updated).
	var changed []kevVulnRaw
	for _, v := range catalog.Vulnerabilities {
		if _, exists := existingIDs[v.CVEID]; !exists {
			changed = append(changed, v)
		}
	}

	// If catalog version changed but no new IDs, existing entries may have been updated.
	// Re-enqueue all to catch field updates (ON CONFLICT handles idempotency).
	if len(changed) == 0 && checkpoint != "" {
		changed = catalog.Vulnerabilities
	}

	if len(changed) == 0 {
		log.Info("no kev changes")
		return FetchResult{NewCheckpoint: catalog.CatalogVersion}, nil
	}

	outDir := filepath.Join(dataDir, "kev")
	os.MkdirAll(outDir, 0o755) //nolint:errcheck

	batchName := fmt.Sprintf("batch-%s.json", time.Now().UTC().Format("2006-01-02T150405"))
	batchPath := filepath.Join(outDir, batchName)

	batch := map[string]any{"entries": changed}
	batchData, err := json.Marshal(batch)
	if err != nil {
		return FetchResult{}, fmt.Errorf("marshal kev batch: %w", err)
	}
	if err := os.WriteFile(batchPath, batchData, 0o644); err != nil {
		return FetchResult{}, err
	}

	key := "kev/" + batchName
	log.Info("kev changes detected", "changed", len(changed), "total", len(catalog.Vulnerabilities))

	return FetchResult{
		ChangedFiles:  []string{key},
		NewCheckpoint: catalog.CatalogVersion,
		ItemCount:     len(changed),
	}, nil
}

const kevURL = "https://www.cisa.gov/sites/default/files/feeds/known_exploited_vulnerabilities.json"

func (k *KEVSource) download(ctx context.Context) (*kevCatalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, kevURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download kev: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("kev status %d: %s", resp.StatusCode, string(body))
	}

	var catalog kevCatalog
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode kev: %w", err)
	}

	return &catalog, nil
}
