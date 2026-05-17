package parser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

// OSVParser handles the OSV schema used by GHSA, RUSTSEC, govuln, PyPA, PSF, and OSV bucket.
// See: https://ossf.github.io/osv-schema/
type OSVParser struct{}

// osvRecord is the raw OSV JSON structure.
type osvRecord struct {
	ID        string     `json:"id"`
	Aliases   []string   `json:"aliases"`
	Summary   string     `json:"summary"`
	Details   string     `json:"details"`
	Modified  string     `json:"modified"`
	Published string     `json:"published"`
	Withdrawn string     `json:"withdrawn"`
	Severity  []osvSev   `json:"severity"`
	Affected  []osvAff   `json:"affected"`
	Refs      []osvRef   `json:"references"`
	DBSpec    any        `json:"database_specific"`
}

type osvSev struct {
	Type  string `json:"type"`
	Score string `json:"score"`
}

type osvAff struct {
	Package  osvPkg   `json:"package"`
	Ranges   []osvRng `json:"ranges"`
	Versions []string `json:"versions"`
	DBSpec   any      `json:"database_specific"`
	EcoSpec  any      `json:"ecosystem_specific"`
}

type osvPkg struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Purl      string `json:"purl"`
}

type osvRng struct {
	Type   string      `json:"type"`
	Events []osvEvent  `json:"events"`
}

type osvEvent struct {
	Introduced   string `json:"introduced,omitempty"`
	Fixed        string `json:"fixed,omitempty"`
	LastAffected string `json:"last_affected,omitempty"`
	Limit        string `json:"limit,omitempty"`
}

type osvRef struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (p *OSVParser) Parse(data []byte) ([]model.Vulnerability, error) {
	var rec osvRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal osv: %w", err)
	}

	v := model.Vulnerability{
		ID:               rec.ID,
		Aliases:          rec.Aliases,
		Summary:          rec.Summary,
		Details:          rec.Details,
		Published:        parseTime(rec.Published),
		Modified:         parseTime(rec.Modified),
		Withdrawn:        parseTime(rec.Withdrawn),
		DatabaseSpecific: rec.DBSpec,
	}

	for _, s := range rec.Severity {
		sev := model.Severity{Type: s.Type}
		if strings.HasPrefix(s.Score, "CVSS:") {
			sev.Vector = s.Score
		} else {
			sev.Score = s.Score
		}
		v.Severity = append(v.Severity, sev)
	}

	for _, r := range rec.Refs {
		v.References = append(v.References, model.Reference{
			Type: r.Type,
			URL:  r.URL,
		})
	}

	for _, aff := range rec.Affected {
		dbSpec := mergeDBSpec(aff.DBSpec, aff.EcoSpec)
		ap := model.AffectedPackage{
			Ecosystem:        aff.Package.Ecosystem,
			PackageName:      aff.Package.Name,
			Purl:             aff.Package.Purl,
			Versions:         aff.Versions,
			DatabaseSpecific: dbSpec,
		}

		for _, rng := range aff.Ranges {
			ranges, flags := convertOSVRange(rng)
			ap.VersionRanges = append(ap.VersionRanges, ranges...)
			ap.QualityFlags = appendUnique(ap.QualityFlags, flags...)
		}

		if len(ap.VersionRanges) == 0 && len(ap.Versions) == 0 {
			ap.QualityFlags = appendUnique(ap.QualityFlags, "empty_range")
		}

		v.AffectedPackages = append(v.AffectedPackages, ap)
	}

	return []model.Vulnerability{v}, nil
}

func convertOSVRange(rng osvRng) ([]model.VersionRange, []string) {
	var ranges []model.VersionRange
	var flags []string

	var current model.VersionRange
	for _, ev := range rng.Events {
		switch {
		case ev.Introduced != "":
			if current.Introduced != "" {
				if current.Fixed == "" && current.LastAffected == "" {
					flags = appendUnique(flags, "no_upper_bound")
				}
				ranges = append(ranges, current)
			}
			current = model.VersionRange{RangeType: rng.Type, Introduced: ev.Introduced}
			if ev.Introduced == "0" {
				flags = appendUnique(flags, "unbounded_range")
			}
		case ev.Fixed != "":
			current.Fixed = ev.Fixed
		case ev.Limit != "":
			current.Fixed = ev.Limit
		case ev.LastAffected != "":
			current.LastAffected = ev.LastAffected
		}
	}

	if current.Introduced != "" {
		if current.Fixed == "" && current.LastAffected == "" {
			flags = appendUnique(flags, "no_upper_bound")
		}
		ranges = append(ranges, current)
	}

	return ranges, flags
}

func appendUnique(slice []string, items ...string) []string {
	seen := make(map[string]bool, len(slice))
	for _, s := range slice {
		seen[s] = true
	}
	for _, item := range items {
		if !seen[item] {
			slice = append(slice, item)
			seen[item] = true
		}
	}
	return slice
}

func mergeDBSpec(dbSpec, ecoSpec any) any {
	if ecoSpec == nil {
		return dbSpec
	}
	if dbSpec == nil {
		return map[string]any{"ecosystem_specific": ecoSpec}
	}
	if m, ok := dbSpec.(map[string]any); ok {
		m["ecosystem_specific"] = ecoSpec
		return m
	}
	return map[string]any{"database_specific": dbSpec, "ecosystem_specific": ecoSpec}
}
