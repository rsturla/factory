package source

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EPSSIndex is the subset of store needed for EPSS diffing.
type EPSSIndex interface {
	GetAllEPSSScoreMap(ctx context.Context) (map[string]float32, error)
}

// EPSSSource downloads EPSS scores, diffs against DB, writes batch of changes.
type EPSSSource struct {
	index      EPSSIndex
	httpClient *http.Client
}

func NewEPSSSource(idx EPSSIndex) *EPSSSource {
	return &EPSSSource{
		index:      idx,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (e *EPSSSource) Name() string { return "epss" }

const epssURL = "https://epss.empiricalsecurity.com/epss_scores-current.csv.gz"

type epssEntry struct {
	CVE        string  `json:"cve"`
	EPSS       float32 `json:"epss"`
	Percentile float32 `json:"percentile"`
}

func (e *EPSSSource) Fetch(ctx context.Context, dataDir string, checkpoint string) (FetchResult, error) {
	log := slog.With("source", "epss")

	entries, modelVersion, scoreDate, err := e.download(ctx)
	if err != nil {
		return FetchResult{}, err
	}

	if scoreDate == checkpoint {
		log.Info("epss scores unchanged", "date", scoreDate)
		return FetchResult{NewCheckpoint: scoreDate}, nil
	}

	existingScores, err := e.index.GetAllEPSSScoreMap(ctx)
	if err != nil {
		return FetchResult{}, fmt.Errorf("get existing epss: %w", err)
	}

	var changed []epssEntry
	for _, entry := range entries {
		existing, ok := existingScores[entry.CVE]
		if !ok || math.Abs(float64(entry.EPSS-existing)) > 0.01 {
			changed = append(changed, entry)
		}
	}

	if len(changed) == 0 {
		log.Info("no significant epss changes")
		return FetchResult{NewCheckpoint: scoreDate}, nil
	}

	outDir := filepath.Join(dataDir, "epss")
	os.MkdirAll(outDir, 0o755) //nolint:errcheck

	batchName := fmt.Sprintf("batch-%s.json", scoreDate)
	batchPath := filepath.Join(outDir, batchName)

	batch := map[string]any{
		"model_version": modelVersion,
		"score_date":    scoreDate,
		"scores":        changed,
	}
	batchData, err := json.Marshal(batch)
	if err != nil {
		return FetchResult{}, fmt.Errorf("marshal epss batch: %w", err)
	}
	if err := os.WriteFile(batchPath, batchData, 0o644); err != nil {
		return FetchResult{}, err
	}

	key := "epss/" + batchName
	log.Info("epss changes detected", "changed", len(changed), "total", len(entries))

	return FetchResult{
		ChangedFiles:  []string{key},
		NewCheckpoint: scoreDate,
		ItemCount:     len(changed),
	}, nil
}

func (e *EPSSSource) download(ctx context.Context) ([]epssEntry, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, epssURL, nil)
	if err != nil {
		return nil, "", "", err
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("download epss: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", fmt.Errorf("epss status %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, "", "", fmt.Errorf("epss gzip: %w", err)
	}
	defer gz.Close()

	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var entries []epssEntry
	var modelVersion, scoreDate string
	lineNum := 0

	for scanner.Scan() {
		line := scanner.Text()
		lineNum++

		if lineNum == 1 && strings.HasPrefix(line, "#") {
			modelVersion, scoreDate = parseEPSSHeader(line)
			continue
		}

		if lineNum == 2 {
			continue
		}

		parts := strings.SplitN(line, ",", 3)
		if len(parts) < 3 {
			continue
		}

		epss, _ := strconv.ParseFloat(parts[1], 32)
		pctile, _ := strconv.ParseFloat(parts[2], 32)

		if len(entries) >= 500_000 {
			return nil, "", "", fmt.Errorf("epss: exceeded max entries (500000)")
		}
		entries = append(entries, epssEntry{
			CVE:        parts[0],
			EPSS:       float32(epss),
			Percentile: float32(pctile),
		})
	}

	if scoreDate == "" {
		scoreDate = time.Now().UTC().Format("2006-01-02")
	}

	return entries, modelVersion, scoreDate, scanner.Err()
}

func parseEPSSHeader(line string) (string, string) {
	line = strings.TrimPrefix(line, "#")
	var modelVersion, scoreDate string
	for _, part := range strings.Split(line, ",") {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.TrimSpace(kv[0]) {
		case "model_version":
			modelVersion = strings.TrimSpace(kv[1])
		case "score_date":
			scoreDate = strings.TrimSpace(kv[1])
			if t, err := time.Parse("2006-01-02T15:04:05-0700", scoreDate); err == nil {
				scoreDate = t.Format("2006-01-02")
			}
		}
	}
	return modelVersion, scoreDate
}
