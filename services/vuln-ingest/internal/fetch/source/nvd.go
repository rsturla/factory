package source

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// NVDSource fetches from the NVD JSON 2.0 data feeds.
// Bootstrap: downloads all per-year feeds (2002-current).
// Ongoing: HEAD-checks all feeds, downloads only those with changed etags.
type NVDSource struct {
	baseURL    string
	httpClient *http.Client
}

const nvdFeedBase = "https://nvd.nist.gov/feeds/json/cve/2.0"

func NewNVDSource() *NVDSource {
	return &NVDSource{
		baseURL:    nvdFeedBase,
		httpClient: &http.Client{Timeout: 300 * time.Second},
	}
}

func NewNVDSourceWithURL(baseURL string) *NVDSource {
	return &NVDSource{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (n *NVDSource) Name() string { return "nvd" }

// nvdCheckpoint stores etags for each feed to detect changes.
type nvdCheckpoint struct {
	Etags map[string]string `json:"etags"`
}

type nvdFeedResponse struct {
	Vulnerabilities []nvdVulnWrap `json:"vulnerabilities"`
}

type nvdVulnWrap struct {
	CVE json.RawMessage `json:"cve"`
}

func (n *NVDSource) Fetch(ctx context.Context, dataDir string, checkpoint string) (FetchResult, error) {
	log := slog.With("source", "nvd")
	outDir := filepath.Join(dataDir, "nvd")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return FetchResult{}, err
	}

	prev := parseNVDCheckpoint(checkpoint)
	currentYear := time.Now().Year()

	var allKeys []string
	next := nvdCheckpoint{Etags: make(map[string]string)}

	// Check each year feed (2002-current). HEAD to get etag, download only if changed.
	for year := 2002; year <= currentYear; year++ {
		select {
		case <-ctx.Done():
			return FetchResult{}, ctx.Err()
		default:
		}

		label := strconv.Itoa(year)
		feedURL := fmt.Sprintf("%s/nvdcve-2.0-%s.json.gz", n.baseURL, label)

		etag, _ := n.getEtag(ctx, feedURL)
		next.Etags[label] = etag

		if etag != "" && etag == prev.Etags[label] {
			continue
		}

		keys, err := n.downloadFeed(ctx, log, outDir, feedURL, label)
		if err != nil {
			return FetchResult{}, fmt.Errorf("year %s: %w", label, err)
		}
		allKeys = append(allKeys, keys...)
	}

	// Check modified feed — catches recent changes across all years.
	modURL := fmt.Sprintf("%s/nvdcve-2.0-modified.json.gz", n.baseURL)
	modEtag, _ := n.getEtag(ctx, modURL)
	next.Etags["modified"] = modEtag

	if modEtag == "" || modEtag != prev.Etags["modified"] {
		keys, err := n.downloadFeed(ctx, log, outDir, modURL, "modified")
		if err != nil {
			return FetchResult{}, err
		}
		allKeys = append(allKeys, keys...)
	}

	if len(allKeys) == 0 {
		log.Info("no changes across all feeds")
		return FetchResult{NewCheckpoint: checkpoint}, nil
	}

	newCP, _ := json.Marshal(next)
	log.Info("feeds processed", "changed", len(allKeys))
	return FetchResult{
		ChangedFiles:  allKeys,
		NewCheckpoint: string(newCP),
		ItemCount:     len(allKeys),
	}, nil
}

func parseNVDCheckpoint(cp string) nvdCheckpoint {
	var c nvdCheckpoint
	if err := json.Unmarshal([]byte(cp), &c); err != nil || c.Etags == nil {
		return nvdCheckpoint{Etags: make(map[string]string)}
	}
	return c
}

func (n *NVDSource) downloadFeed(ctx context.Context, log *slog.Logger, outDir, feedURL, label string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", label, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("feed %s status %d: %s", label, resp.StatusCode, string(body))
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gzip %s: %w", label, err)
	}
	defer gz.Close()

	var feed nvdFeedResponse
	if err := json.NewDecoder(io.LimitReader(gz, 2<<30)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", label, err)
	}

	var keys []string
	for _, vw := range feed.Vulnerabilities {
		var idHolder struct {
			ID string `json:"id"`
		}
		json.Unmarshal(vw.CVE, &idHolder) //nolint:errcheck
		if idHolder.ID == "" {
			continue
		}

		filePath := filepath.Join(outDir, idHolder.ID+".json")
		if err := os.WriteFile(filePath, vw.CVE, 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", idHolder.ID, err)
		}

		keys = append(keys, "nvd/"+idHolder.ID+".json")
	}

	log.Info("downloaded feed", "feed", label, "cves", len(keys))
	return keys, nil
}

func (n *NVDSource) getEtag(ctx context.Context, feedURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, feedURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()

	return resp.Header.Get("Etag"), nil
}
