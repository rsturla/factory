package parser

import (
	"encoding/json"
	"fmt"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

// NVDOverridesParser handles Anchore's nvd-data-overrides format.
// These provide CPE configurations for CVEs not yet analyzed by NVD.
// Format: {"_annotation": {cve_id, description, ...}, "cve": {"configurations": [...]}}
type NVDOverridesParser struct{}

type nvdOverrideRecord struct {
	Annotation nvdOverrideAnnotation `json:"_annotation"`
	CVE        nvdOverrideCVE        `json:"cve"`
}

type nvdOverrideAnnotation struct {
	CVEID       string   `json:"cve_id"`
	Description string   `json:"description"`
	Modified    string   `json:"modified"`
	Published   string   `json:"published"`
	Reason      string   `json:"reason"`
	References  []string `json:"references"`
}

type nvdOverrideCVE struct {
	Configurations []nvdConfig `json:"configurations"`
}

func (p *NVDOverridesParser) Parse(data []byte) ([]model.Vulnerability, error) {
	var rec nvdOverrideRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal nvd override: %w", err)
	}

	if rec.Annotation.CVEID == "" {
		return nil, fmt.Errorf("nvd override missing cve_id")
	}

	v := model.Vulnerability{
		ID:       rec.Annotation.CVEID,
		Summary:  truncate(rec.Annotation.Description, 256),
		Details:  rec.Annotation.Description,
		Modified: parseTime(rec.Annotation.Modified),
	}

	for _, ref := range rec.Annotation.References {
		v.References = append(v.References, model.Reference{
			Type: "WEB",
			URL:  ref,
		})
	}

	v.AffectedPackages = extractNVDAffected(rec.CVE.Configurations)

	return []model.Vulnerability{v}, nil
}
