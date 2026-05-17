package parser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

// NVDParser handles the NVD 2.0 JSON format.
type NVDParser struct{}

type nvdCVE struct {
	ID               string        `json:"id"`
	Published        string        `json:"published"`
	LastModified     string        `json:"lastModified"`
	VulnStatus       string        `json:"vulnStatus"`
	Descriptions     []nvdDesc     `json:"descriptions"`
	Metrics          nvdMetrics    `json:"metrics"`
	Configurations   []nvdConfig   `json:"configurations"`
	References       []nvdRef      `json:"references"`
	Weaknesses       []nvdWeakness `json:"weaknesses"`
}

type nvdDesc struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type nvdMetrics struct {
	CvssV31 []nvdCVSSWrap   `json:"cvssMetricV31"`
	CvssV30 []nvdCVSSWrap   `json:"cvssMetricV30"`
	CvssV40 []nvdCVSSWrap   `json:"cvssMetricV40"`
	CvssV2  []nvdCVSSV2Wrap `json:"cvssMetricV2"`
}

type nvdCVSSV2Wrap struct {
	Source   string    `json:"source"`
	Type     string    `json:"type"`
	CvssData nvdCVSSV2 `json:"cvssV2"`
}

type nvdCVSSV2 struct {
	Version      string  `json:"version"`
	VectorString string  `json:"vectorString"`
	BaseScore    float64 `json:"baseScore"`
}

type nvdCVSSWrap struct {
	Source   string  `json:"source"`
	Type     string  `json:"type"`
	CvssData nvdCVSS `json:"cvssData"`
}

type nvdCVSS struct {
	Version      string  `json:"version"`
	VectorString string  `json:"vectorString"`
	BaseScore    float64 `json:"baseScore"`
}

type nvdConfig struct {
	Nodes []nvdNode `json:"nodes"`
}

type nvdNode struct {
	Operator string     `json:"operator"`
	Negate   bool       `json:"negate"`
	CpeMatch []nvdMatch `json:"cpeMatch"`
	Children []nvdNode  `json:"children"`
}

type nvdMatch struct {
	Vulnerable            bool   `json:"vulnerable"`
	Criteria              string `json:"criteria"`
	VersionStartIncluding string `json:"versionStartIncluding"`
	VersionStartExcluding string `json:"versionStartExcluding"`
	VersionEndIncluding   string `json:"versionEndIncluding"`
	VersionEndExcluding   string `json:"versionEndExcluding"`
}

type nvdRef struct {
	URL    string   `json:"url"`
	Source string   `json:"source"`
	Tags   []string `json:"tags"`
}

type nvdWeakness struct {
	Source      string    `json:"source"`
	Type        string    `json:"type"`
	Description []nvdDesc `json:"description"`
}

func (p *NVDParser) Parse(data []byte) ([]model.Vulnerability, error) {
	var cve nvdCVE
	if err := json.Unmarshal(data, &cve); err != nil {
		return nil, fmt.Errorf("unmarshal nvd: %w", err)
	}

	v := model.Vulnerability{
		ID:        cve.ID,
		Published: parseTime(cve.Published),
		Modified:  parseTime(cve.LastModified),
	}

	if cve.VulnStatus == "Rejected" {
		v.Withdrawn = v.Modified
		return []model.Vulnerability{v}, nil
	}

	for _, d := range cve.Descriptions {
		if d.Lang == "en" {
			v.Details = d.Value
			v.Summary = truncate(d.Value, 256)
			break
		}
	}

	v.Severity = extractNVDSeverity(cve.Metrics)

	for _, ref := range cve.References {
		v.References = append(v.References, model.Reference{
			Type: nvdRefType(ref.Tags),
			URL:  ref.URL,
		})
	}

	v.AffectedPackages = extractNVDAffected(cve.Configurations)

	cwes := extractCWEs(cve.Weaknesses)
	if len(cwes) > 0 {
		v.DatabaseSpecific = map[string]any{"cwes": cwes}
	}

	return []model.Vulnerability{v}, nil
}

func extractNVDSeverity(m nvdMetrics) []model.Severity {
	var result []model.Severity

	sources := [][]nvdCVSSWrap{m.CvssV31, m.CvssV40, m.CvssV30}
	for _, wraps := range sources {
		for _, w := range wraps {
			result = append(result, model.Severity{
				Type:   fmt.Sprintf("CVSS_V%s", strings.ReplaceAll(w.CvssData.Version, ".", "_")),
				Score:  fmt.Sprintf("%.1f", w.CvssData.BaseScore),
				Vector: w.CvssData.VectorString,
			})
		}
	}

	for _, w := range m.CvssV2 {
		result = append(result, model.Severity{
			Type:   "CVSS_V2_0",
			Score:  fmt.Sprintf("%.1f", w.CvssData.BaseScore),
			Vector: w.CvssData.VectorString,
		})
	}

	return result
}

func extractNVDAffected(configs []nvdConfig) []model.AffectedPackage {
	var result []model.AffectedPackage

	for _, cfg := range configs {
		for _, node := range cfg.Nodes {
			result = append(result, extractNodeAffected(node)...)
		}
	}
	return result
}

func extractNodeAffected(node nvdNode) []model.AffectedPackage {
	var result []model.AffectedPackage

	for _, match := range node.CpeMatch {
		if !match.Vulnerable {
			continue
		}

		ap := model.AffectedPackage{
			DatabaseSpecific: map[string]any{"cpe": match.Criteria},
		}

		parts := strings.Split(match.Criteria, ":")
		if len(parts) >= 5 {
			ap.PackageName = parts[4]
			ap.Vendor = parts[3]
		}

		var flags []string
		vr := model.VersionRange{}

		if match.VersionStartIncluding != "" {
			vr.Introduced = match.VersionStartIncluding
		} else if match.VersionStartExcluding != "" {
			vr.Introduced = match.VersionStartExcluding
			vr.IntroducedExclusive = true
		}

		if match.VersionEndExcluding != "" {
			vr.Fixed = match.VersionEndExcluding
		} else if match.VersionEndIncluding != "" {
			vr.LastAffected = match.VersionEndIncluding
		}

		if vr.Introduced == "" && vr.Fixed == "" && vr.LastAffected == "" {
			if len(parts) >= 6 && parts[5] != "*" {
				ap.Versions = []string{parts[5]}
			} else {
				vr.Introduced = "0"
				vr.Fixed = "*"
				ap.VersionRanges = []model.VersionRange{vr}
				flags = append(flags, "all_versions_affected")
			}
		} else {
			if vr.Fixed == "" && vr.LastAffected == "" {
				flags = append(flags, "no_upper_bound")
			}
			ap.VersionRanges = []model.VersionRange{vr}
		}

		ap.QualityFlags = flags
		result = append(result, ap)
	}

	for _, child := range node.Children {
		result = append(result, extractNodeAffected(child)...)
	}

	return result
}

func nvdRefType(tags []string) string {
	for _, tag := range tags {
		switch tag {
		case "Patch":
			return "FIX"
		case "Vendor Advisory":
			return "ADVISORY"
		case "Exploit":
			return "EVIDENCE"
		}
	}
	return "WEB"
}

func extractCWEs(weaknesses []nvdWeakness) []string {
	var cwes []string
	for _, w := range weaknesses {
		for _, d := range w.Description {
			if d.Lang == "en" && d.Value != "NVD-CWE-noinfo" && d.Value != "NVD-CWE-Other" {
				cwes = append(cwes, d.Value)
			}
		}
	}
	return cwes
}
