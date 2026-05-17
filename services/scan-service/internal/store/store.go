package store

import (
	"context"

	"github.com/rsturla/factory/services/scan-service/internal/model"
)

type Store interface {
	// Scans
	UpsertScan(ctx context.Context, scan model.Scan) error
	GetLatestScan(ctx context.Context, platformID, scanner string) (*model.Scan, error)
	ListScansByImage(ctx context.Context, imageDigest string) ([]model.Scan, error)

	// Findings
	UpsertFindings(ctx context.Context, scanID string, findings []model.Finding) error
	ListFindings(ctx context.Context, scanID string) ([]model.Finding, error)
	ListFindingsByPlatform(ctx context.Context, platformID, scanner string) ([]model.Finding, error)

	// Scanner DB state
	GetDBState(ctx context.Context, scanner string) (*model.ScannerDBState, error)
	UpsertDBState(ctx context.Context, state model.ScannerDBState) error

	// Health
	Ping(ctx context.Context) error
	Close()
}
