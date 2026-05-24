package store

import (
	"context"
	"time"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
)

type Store interface {
	// Vulnerabilities — source scopes the affected_packages delete to avoid cross-source clobbering.
	UpsertVulnerability(ctx context.Context, v *model.Vulnerability, source string) error
	GetVulnerability(ctx context.Context, id string) (*model.Vulnerability, error)
	GetRelatedVulnerabilities(ctx context.Context, id string) ([]*model.Vulnerability, error)
	ListVulnerabilities(ctx context.Context, opts ListOpts) ([]*model.Vulnerability, error)
	BatchGetVulnerabilities(ctx context.Context, ids []string) ([]*model.Vulnerability, error)

	// Affected packages
	ListAffectedByPackage(ctx context.Context, ecosystem, packageName string, opts ListOpts) ([]*model.Vulnerability, error)
	ListAffectedByPurl(ctx context.Context, purl string, opts ListOpts) ([]*model.Vulnerability, error)
	BatchQueryAffected(ctx context.Context, queries []AffectedQuery, opts ListOpts) (map[string][]*model.Vulnerability, error)

	// Counts
	CountVulnerabilities(ctx context.Context, opts ListOpts) (int, error)
	CountAffectedByPackage(ctx context.Context, ecosystem, packageName string) (int, error)

	// Source records
	UpsertSourceRecord(ctx context.Context, rec *model.SourceRecord) error
	GetSourceRecord(ctx context.Context, vulnID, source string) (*model.SourceRecord, error)

	// Checkpoints
	GetCheckpoint(ctx context.Context, source string) (*model.SourceCheckpoint, error)
	UpdateCheckpoint(ctx context.Context, source, checkpointValue string, itemsSynced int64) error
	SetCheckpointError(ctx context.Context, source, errMsg string) error
	ListCheckpoints(ctx context.Context) ([]*model.SourceCheckpoint, error)

	// Enrichment
	UpsertKEVEntries(ctx context.Context, entries []model.KEVEntry) error
	GetKEVEntry(ctx context.Context, cveID string) (*model.KEVEntry, error)

	UpsertEPSSScores(ctx context.Context, scores []model.EPSSScore) error
	GetEPSSScore(ctx context.Context, cveID string) (*model.EPSSScore, error)

	UpsertVendorNotes(ctx context.Context, notes []model.VendorNote) error
	GetVendorNotes(ctx context.Context, cveID string) ([]model.VendorNote, error)

	// Enrichment diff helpers
	GetAllEPSSScoreMap(ctx context.Context) (map[string]float32, error)
	GetAllKEVIDs(ctx context.Context) (map[string]time.Time, error)
	GetVendorNoteCVEIDs(ctx context.Context, vendor string) (map[string]time.Time, error)

	// Health
	Ping(ctx context.Context) error
	Close()
}

type ListOpts struct {
	Limit         int
	Offset        int
	ModifiedSince *time.Time
	UpdatedSince  *time.Time
}

type AffectedQuery struct {
	Ecosystem   string `json:"ecosystem,omitempty"`
	PackageName string `json:"package_name,omitempty"`
	Vendor      string `json:"vendor,omitempty"`
	Purl        string `json:"purl,omitempty"`
}
