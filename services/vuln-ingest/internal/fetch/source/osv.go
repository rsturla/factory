package source

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/blob"
)

// OSVSource fetches from the OSV GCS bucket via all.zip downloads per ecosystem.
type OSVSource struct {
	ecosystems []string
	httpClient *http.Client
}

func NewOSVSource(ecosystems []string) *OSVSource {
	return &OSVSource{
		ecosystems: ecosystems,
		httpClient: &http.Client{Timeout: 300 * time.Second},
	}
}

func (o *OSVSource) Name() string { return "osv" }

// osvCheckpoint stores etag per ecosystem for change detection.
type osvCheckpoint struct {
	Etags map[string]string `json:"etags"`
}

func (o *OSVSource) Fetch(ctx context.Context, blobs blob.Store, checkpoint string) (FetchResult, error) {
	log := slog.With("source", "osv")

	prev := parseOSVCheckpoint(checkpoint)
	next := osvCheckpoint{Etags: make(map[string]string)}
	var allKeys []string

	for _, eco := range o.ecosystems {
		select {
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		default:
		}

		zipURL := fmt.Sprintf("https://osv-vulnerabilities.storage.googleapis.com/%s/all.zip", eco)

		etag, _ := o.getEtag(ctx, zipURL)
		next.Etags[eco] = etag

		if etag != "" && etag == prev.Etags[eco] {
			log.Info("ecosystem unchanged", "ecosystem", eco)
			continue
		}

		keys, err := o.fetchEcosystem(ctx, log, blobs, eco, zipURL)
		if err != nil {
			log.Error("fetch ecosystem failed", "ecosystem", eco, "error", err)
			continue
		}
		allKeys = append(allKeys, keys...)
	}

	if len(allKeys) == 0 {
		log.Info("no changes across ecosystems")
		return FetchResult{NewCheckpoint: checkpoint}, nil
	}

	newCP, _ := json.Marshal(next)
	log.Info("osv fetch complete", "changed", len(allKeys))
	return FetchResult{
		ChangedFiles:  allKeys,
		NewCheckpoint: string(newCP),
		ItemCount:     len(allKeys),
	}, nil
}

func (o *OSVSource) fetchEcosystem(ctx context.Context, log *slog.Logger, blobs blob.Store, ecosystem, zipURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ecosystem, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s status %d", ecosystem, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<30))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ecosystem, err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("zip %s: %w", ecosystem, err)
	}

	var keys []string
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".json") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 16<<20))
		rc.Close()
		if err != nil {
			continue
		}

		key := "osv/" + strings.ToLower(ecosystem) + "/" + f.Name
		if err := blobs.Put(ctx, key, data); err != nil {
			return nil, fmt.Errorf("write %s: %w", f.Name, err)
		}

		keys = append(keys, key)
	}

	log.Info("downloaded ecosystem", "ecosystem", ecosystem, "entries", len(keys))
	return keys, nil
}

func (o *OSVSource) getEtag(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	return resp.Header.Get("Etag"), nil
}

func parseOSVCheckpoint(cp string) osvCheckpoint {
	var c osvCheckpoint
	if err := json.Unmarshal([]byte(cp), &c); err != nil || c.Etags == nil {
		return osvCheckpoint{Etags: make(map[string]string)}
	}
	return c
}
