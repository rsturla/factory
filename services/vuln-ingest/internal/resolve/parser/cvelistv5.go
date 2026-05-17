package parser

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

// CVEListV5Parser handles the CVE List V5 JSON format from https://github.com/CVEProject/cvelistV5
type CVEListV5Parser struct{}

type cveV5Record struct {
	DataType    string      `json:"dataType"`
	DataVersion string      `json:"dataVersion"`
	CVEMetadata cveV5Meta   `json:"cveMetadata"`
	Containers  cveV5Cont   `json:"containers"`
}

type cveV5Meta struct {
	CVEID       string `json:"cveId"`
	State       string `json:"state"`
	DateUpdated string `json:"dateUpdated"`
	DatePublished string `json:"datePublished"`
}

type cveV5Cont struct {
	CNA cveV5CNA   `json:"cna"`
	ADP []cveV5CNA `json:"adp"`
}

type cveV5CNA struct {
	Title            string            `json:"title"`
	ProviderMetadata cveV5ProviderMeta `json:"providerMetadata"`
	Descriptions     []cveV5Desc       `json:"descriptions"`
	Affected         []cveV5Aff        `json:"affected"`
	References       []cveV5Ref        `json:"references"`
	Metrics          []cveV5Metric     `json:"metrics"`
	ProblemTypes     []cveV5Prob       `json:"problemTypes"`
}

type cveV5ProviderMeta struct {
	OrgID     string `json:"orgId"`
	ShortName string `json:"shortName"`
}

type cveV5Desc struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type cveV5Aff struct {
	Vendor        string         `json:"vendor"`
	Product       string         `json:"product"`
	CollectionURL string         `json:"collectionURL"`
	PackageName   string         `json:"packageName"`
	Versions      []cveV5Ver     `json:"versions"`
	DefaultStatus string         `json:"defaultStatus"`
	Platforms     []string       `json:"platforms"`
	Modules       []string       `json:"modules"`
	CPES          []string       `json:"cpes"`
}

type cveV5Ver struct {
	Version    string `json:"version"`
	Status     string `json:"status"`
	LessThan   string `json:"lessThan"`
	LessEqual  string `json:"lessThanOrEqual"`
	VersionType string `json:"versionType"`
}

type cveV5Ref struct {
	URL  string   `json:"url"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type cveV5Metric struct {
	CvssV31 *cveV5CVSS `json:"cvssV3_1"`
	CvssV30 *cveV5CVSS `json:"cvssV3_0"`
	CvssV40 *cveV5CVSS `json:"cvssV4_0"`
}

type cveV5CVSS struct {
	Version      string  `json:"version"`
	VectorString string  `json:"vectorString"`
	BaseScore    float64 `json:"baseScore"`
}

type cveV5Prob struct {
	Descriptions []cveV5Desc `json:"descriptions"`
}

func (p *CVEListV5Parser) Parse(data []byte) ([]model.Vulnerability, error) {
	var rec cveV5Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("unmarshal cve v5: %w", err)
	}

	if rec.CVEMetadata.State == "REJECTED" {
		withdrawn := parseTime(rec.CVEMetadata.DateUpdated)
		return []model.Vulnerability{{
			ID:        rec.CVEMetadata.CVEID,
			Withdrawn: withdrawn,
			Modified:  withdrawn,
		}}, nil
	}

	v := model.Vulnerability{
		ID:        rec.CVEMetadata.CVEID,
		Published: parseTime(rec.CVEMetadata.DatePublished),
		Modified:  parseTime(rec.CVEMetadata.DateUpdated),
	}

	cna := rec.Containers.CNA

	if cna.Title != "" {
		v.Summary = cna.Title
	}
	for _, d := range cna.Descriptions {
		if d.Lang == "en" || strings.HasPrefix(d.Lang, "en-") {
			v.Details = d.Value
			if v.Summary == "" {
				v.Summary = truncate(d.Value, 256)
			}
			break
		}
	}

	for _, m := range cna.Metrics {
		cvss := m.CvssV31
		if cvss == nil {
			cvss = m.CvssV30
		}
		if cvss == nil {
			cvss = m.CvssV40
		}
		if cvss != nil {
			v.Severity = append(v.Severity, model.Severity{
				Type:   fmt.Sprintf("CVSS_V%s", strings.ReplaceAll(cvss.Version, ".", "_")),
				Score:  fmt.Sprintf("%.1f", cvss.BaseScore),
				Vector: cvss.VectorString,
			})
		}
	}

	for _, ref := range cna.References {
		v.References = append(v.References, model.Reference{
			Type: refType(ref.Tags),
			URL:  ref.URL,
		})
	}

	v.AffectedPackages = append(v.AffectedPackages, extractV5Affected(cna.Affected, "")...)

	for _, adp := range rec.Containers.ADP {
		adpSource := adp.ProviderMetadata.ShortName
		if adpSource == "" {
			adpSource = adp.Title
		}
		v.AffectedPackages = append(v.AffectedPackages, extractV5Affected(adp.Affected, adpSource)...)
		for _, m := range adp.Metrics {
			cvss := m.CvssV31
			if cvss == nil {
				cvss = m.CvssV30
			}
			if cvss == nil {
				cvss = m.CvssV40
			}
			if cvss != nil {
				v.Severity = append(v.Severity, model.Severity{
					Type:   fmt.Sprintf("CVSS_V%s", strings.ReplaceAll(cvss.Version, ".", "_")),
					Score:  fmt.Sprintf("%.1f", cvss.BaseScore),
					Vector: cvss.VectorString,
				})
			}
		}
	}

	return []model.Vulnerability{v}, nil
}

func extractV5Affected(affs []cveV5Aff, source string) []model.AffectedPackage {
	var result []model.AffectedPackage

	for _, aff := range affs {
		ap := model.AffectedPackage{
			PackageName: packageName(aff),
			Vendor:      aff.Vendor,
			Source:       source,
		}

		if aff.CollectionURL != "" {
			ap.Ecosystem = ecosystemFromURL(aff.CollectionURL)
		}

		if aff.DefaultStatus == "affected" {
			extractDefaultAffected(&ap, aff.Versions)
		} else {
			extractExplicitAffected(&ap, aff.Versions)
		}

		if len(aff.CPES) > 0 {
			ap.DatabaseSpecific = map[string]any{"cpes": aff.CPES}
		}

		if len(aff.Versions) == 0 {
			ap.QualityFlags = appendUnique(ap.QualityFlags, "empty_range")
			if aff.DefaultStatus == "affected" {
				ap.QualityFlags = appendUnique(ap.QualityFlags, "unbounded_range")
			}
		}

		result = append(result, ap)
	}
	return result
}

// extractExplicitAffected handles the standard case where defaultStatus is
// unaffected or empty: only "status: affected" entries define vulnerable ranges.
func extractExplicitAffected(ap *model.AffectedPackage, versions []cveV5Ver) {
	for _, ver := range versions {
		if ver.Status != "affected" {
			continue
		}
		vr := model.VersionRange{
			Introduced: ver.Version,
			RangeType:  ver.VersionType,
		}
		switch {
		case ver.LessThan != "":
			vr.Fixed = ver.LessThan
		case ver.LessEqual != "":
			vr.LastAffected = ver.LessEqual
		default:
			ap.QualityFlags = appendUnique(ap.QualityFlags, "no_upper_bound")
		}
		ap.VersionRanges = append(ap.VersionRanges, vr)
	}
}

// extractDefaultAffected handles defaultStatus == "affected". In this mode,
// affected entries mark introduction points and unaffected entries with
// lessThan/lessThanOrEqual mark fix boundaries.
func extractDefaultAffected(ap *model.AffectedPackage, versions []cveV5Ver) {
	// Collect affected (introduction) and unaffected (fix) entries separately.
	type affectedEntry struct {
		version string
		matched bool
	}
	var introduced []affectedEntry
	var fixes []cveV5Ver

	for _, ver := range versions {
		switch ver.Status {
		case "affected":
			introduced = append(introduced, affectedEntry{version: ver.Version})
		case "unaffected":
			if ver.LessThan != "" || ver.LessEqual != "" {
				fixes = append(fixes, ver)
			}
		}
	}

	// If there are no explicit affected entries but defaultStatus is affected,
	// every unaffected fix boundary still defines a range from "0" to the fix.
	if len(introduced) == 0 && len(fixes) > 0 {
		for _, fix := range fixes {
			vr := model.VersionRange{
				Introduced: "0",
				RangeType:  fix.VersionType,
			}
			if fix.LessThan != "" {
				vr.Fixed = fix.LessThan
			} else {
				vr.Fixed = fix.LessEqual
			}
			ap.VersionRanges = append(ap.VersionRanges, vr)
		}
		return
	}

	// Match each fix to its introduced version. A fix entry with lessThanOrEqual
	// like "6.1.*" matches introduced "6.1" (the fix version itself is the
	// value in `version`). We try prefix matching on the lessThanOrEqual field
	// and fall back to sequential pairing.
	for i := range introduced {
		for j := range fixes {
			fix := fixes[j]
			// Match: fix.LessEqual pattern "X.Y.*" corresponds to introduced "X.Y",
			// or fix.version starts with the introduced version.
			leStr := fix.LessEqual
			if leStr == "" {
				leStr = fix.LessThan
			}
			prefix := introduced[i].version
			if strings.HasPrefix(leStr, prefix+".") || strings.HasPrefix(fix.Version, prefix+".") || fix.Version == prefix {
				vr := model.VersionRange{
					Introduced: introduced[i].version,
					RangeType:  fix.VersionType,
				}
				if fix.LessEqual != "" {
					// lessThanOrEqual with a concrete version means that version is the fix.
					vr.Fixed = fix.Version
				} else {
					vr.Fixed = fix.LessThan
				}
				ap.VersionRanges = append(ap.VersionRanges, vr)
				introduced[i].matched = true
				break
			}
		}
	}

	// Any introduced version without a matching fix gets no_upper_bound.
	for _, entry := range introduced {
		if !entry.matched {
			vr := model.VersionRange{Introduced: entry.version}
			ap.VersionRanges = append(ap.VersionRanges, vr)
			ap.QualityFlags = appendUnique(ap.QualityFlags, "no_upper_bound")
		}
	}
}

func packageName(aff cveV5Aff) string {
	if aff.PackageName != "" {
		return aff.PackageName
	}
	return aff.Product
}

func ecosystemFromURL(url string) string {
	switch {
	case strings.Contains(url, "npmjs.com"):
		return "npm"
	case strings.Contains(url, "pypi.org"):
		return "PyPI"
	case strings.Contains(url, "crates.io"):
		return "crates.io"
	case strings.Contains(url, "pkg.go.dev"):
		return "Go"
	case strings.Contains(url, "rubygems.org"):
		return "RubyGems"
	case strings.Contains(url, "repo1.maven.org"):
		return "Maven"
	case strings.Contains(url, "nuget.org"):
		return "NuGet"
	default:
		return ""
	}
}

func refType(tags []string) string {
	for _, tag := range tags {
		switch tag {
		case "vendor-advisory":
			return "ADVISORY"
		case "patch":
			return "FIX"
		case "exploit":
			return "EVIDENCE"
		}
	}
	return "WEB"
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}
