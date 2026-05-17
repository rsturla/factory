package parser

import (
	"encoding/json"
	"fmt"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

type epssBatch struct {
	ModelVersion string      `json:"model_version"`
	ScoreDate    string      `json:"score_date"`
	Scores       []epssScore `json:"scores"`
}

type epssScore struct {
	CVE        string  `json:"cve"`
	EPSS       float32 `json:"epss"`
	Percentile float32 `json:"percentile"`
}

// ParseEPSSBatch extracts EPSS scores from a batch file.
func ParseEPSSBatch(data []byte) ([]model.EPSSScore, string, error) {
	var batch epssBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		return nil, "", fmt.Errorf("unmarshal epss batch: %w", err)
	}

	scoreDate := parseTime(batch.ScoreDate)

	scores := make([]model.EPSSScore, 0, len(batch.Scores))
	for _, s := range batch.Scores {
		scores = append(scores, model.EPSSScore{
			CVEID:        s.CVE,
			Score:        s.EPSS,
			Percentile:   s.Percentile,
			ModelVersion: batch.ModelVersion,
			ScoreDate:    scoreDate,
		})
	}

	return scores, batch.ScoreDate, nil
}
