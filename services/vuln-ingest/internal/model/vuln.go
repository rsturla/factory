package model

import "time"

type Vulnerability struct {
	ID               string            `json:"id"`
	Aliases          []string          `json:"aliases,omitempty"`
	Summary          string            `json:"summary,omitempty"`
	Details          string            `json:"details,omitempty"`
	Severity         []Severity        `json:"severity,omitempty"`
	Published        *time.Time        `json:"published,omitempty"`
	Modified         *time.Time        `json:"modified,omitempty"`
	Withdrawn        *time.Time        `json:"withdrawn,omitempty"`
	References       []Reference       `json:"references,omitempty"`
	AffectedPackages []AffectedPackage `json:"affected,omitempty"`
	DatabaseSpecific any               `json:"database_specific,omitempty"`
}

type Severity struct {
	Type   string `json:"type"`
	Score  string `json:"score"`
	Vector string `json:"vector,omitempty"`
}

type Reference struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

type AffectedPackage struct {
	Source           string         `json:"source,omitempty"`
	Vendor           string         `json:"vendor,omitempty"`
	Ecosystem        string         `json:"ecosystem,omitempty"`
	PackageName      string         `json:"package_name,omitempty"`
	Purl             string         `json:"purl,omitempty"`
	VersionRanges    []VersionRange `json:"version_ranges,omitempty"`
	Versions         []string       `json:"versions,omitempty"`
	DatabaseSpecific any            `json:"database_specific,omitempty"`
	QualityFlags     []string       `json:"quality_flags,omitempty"`
}

type VersionRange struct {
	RangeType           string `json:"range_type,omitempty"`
	Introduced          string `json:"introduced,omitempty"`
	IntroducedExclusive bool   `json:"introduced_exclusive,omitempty"`
	Fixed               string `json:"fixed,omitempty"`
	LastAffected        string `json:"last_affected,omitempty"`
}

type SourceRecord struct {
	VulnID    string    `json:"vuln_id"`
	Source    string    `json:"source"`
	SourceID  string    `json:"source_id,omitempty"`
	RawHash   string    `json:"raw_hash,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
}

type KEVEntry struct {
	CVEID            string     `json:"cve_id"`
	VendorProject    string     `json:"vendor_project,omitempty"`
	Product          string     `json:"product,omitempty"`
	DateAdded        *time.Time `json:"date_added,omitempty"`
	DueDate          *time.Time `json:"due_date,omitempty"`
	ShortDescription string     `json:"short_description,omitempty"`
	RequiredAction   string     `json:"required_action,omitempty"`
	Notes            string     `json:"notes,omitempty"`
}

type EPSSScore struct {
	CVEID        string     `json:"cve_id"`
	Score        float32    `json:"score"`
	Percentile   float32    `json:"percentile"`
	ModelVersion string     `json:"model_version,omitempty"`
	ScoreDate    *time.Time `json:"score_date,omitempty"`
}
