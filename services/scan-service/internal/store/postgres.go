package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/scan-service/internal/model"
)

type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *PGStore) Close() {
	s.pool.Close()
}

// --- Scans ---

func (s *PGStore) UpsertScan(ctx context.Context, scan model.Scan) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scans (id, platform_id, scanner, db_version, started_at, completed_at,
			vuln_count, critical_count, high_count, medium_count, low_count, status, error_message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (id) DO UPDATE SET
			platform_id = EXCLUDED.platform_id,
			scanner = EXCLUDED.scanner,
			db_version = EXCLUDED.db_version,
			started_at = EXCLUDED.started_at,
			completed_at = EXCLUDED.completed_at,
			vuln_count = EXCLUDED.vuln_count,
			critical_count = EXCLUDED.critical_count,
			high_count = EXCLUDED.high_count,
			medium_count = EXCLUDED.medium_count,
			low_count = EXCLUDED.low_count,
			status = EXCLUDED.status,
			error_message = EXCLUDED.error_message
	`, scan.ID, scan.PlatformID, scan.Scanner, scan.DBVersion,
		scan.StartedAt, scan.CompletedAt,
		scan.VulnCount, scan.CriticalCount, scan.HighCount, scan.MediumCount, scan.LowCount,
		scan.Status, scan.ErrorMessage)
	if err != nil {
		return fmt.Errorf("upsert scan: %w", err)
	}
	return nil
}

func (s *PGStore) GetLatestScan(ctx context.Context, platformID, scanner string) (*model.Scan, error) {
	scan := &model.Scan{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, platform_id, scanner, db_version, started_at, completed_at,
			vuln_count, critical_count, high_count, medium_count, low_count,
			status, COALESCE(error_message, '')
		FROM scans
		WHERE platform_id = $1 AND scanner = $2
		ORDER BY completed_at DESC
		LIMIT 1
	`, platformID, scanner).Scan(
		&scan.ID, &scan.PlatformID, &scan.Scanner, &scan.DBVersion,
		&scan.StartedAt, &scan.CompletedAt,
		&scan.VulnCount, &scan.CriticalCount, &scan.HighCount, &scan.MediumCount, &scan.LowCount,
		&scan.Status, &scan.ErrorMessage,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest scan: %w", err)
	}
	return scan, nil
}

func (s *PGStore) ListScansByImage(ctx context.Context, imageDigest string) ([]model.Scan, error) {
	// platform_id contains the digest as a prefix (e.g. "sha256:xxx|linux/amd64")
	rows, err := s.pool.Query(ctx, `
		SELECT id, platform_id, scanner, db_version, started_at, completed_at,
			vuln_count, critical_count, high_count, medium_count, low_count,
			status, COALESCE(error_message, '')
		FROM scans
		WHERE platform_id LIKE $1
		ORDER BY completed_at DESC
	`, imageDigest+"|%")
	if err != nil {
		return nil, fmt.Errorf("list scans by image: %w", err)
	}
	defer rows.Close()

	var scans []model.Scan
	for rows.Next() {
		var scan model.Scan
		if err := rows.Scan(
			&scan.ID, &scan.PlatformID, &scan.Scanner, &scan.DBVersion,
			&scan.StartedAt, &scan.CompletedAt,
			&scan.VulnCount, &scan.CriticalCount, &scan.HighCount, &scan.MediumCount, &scan.LowCount,
			&scan.Status, &scan.ErrorMessage,
		); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		scans = append(scans, scan)
	}
	return scans, rows.Err()
}

// --- Findings ---

func (s *PGStore) UpsertFindings(ctx context.Context, scanID string, findings []model.Finding) error {
	if len(findings) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	batch := &pgx.Batch{}
	for _, f := range findings {
		batch.Queue(`
			INSERT INTO findings (scan_id, vuln_id, severity, package_name, package_version, package_type, fixed_version)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (scan_id, vuln_id, package_name, package_version) DO UPDATE SET
				severity = EXCLUDED.severity,
				package_type = EXCLUDED.package_type,
				fixed_version = EXCLUDED.fixed_version
		`, f.ScanID, f.VulnID, f.Severity, f.PackageName, f.PackageVersion, f.PackageType, f.FixedVersion)
	}

	br := tx.SendBatch(ctx, batch)
	for range findings {
		if _, err := br.Exec(); err != nil {
			br.Close() //nolint:errcheck
			return fmt.Errorf("upsert finding: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close batch: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PGStore) ListFindings(ctx context.Context, scanID string) ([]model.Finding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT scan_id, vuln_id, severity, package_name, package_version,
			COALESCE(package_type, ''), COALESCE(fixed_version, '')
		FROM findings
		WHERE scan_id = $1
		ORDER BY severity, vuln_id, package_name
	`, scanID)
	if err != nil {
		return nil, fmt.Errorf("list findings: %w", err)
	}
	defer rows.Close()

	var findings []model.Finding
	for rows.Next() {
		var f model.Finding
		if err := rows.Scan(
			&f.ScanID, &f.VulnID, &f.Severity, &f.PackageName, &f.PackageVersion,
			&f.PackageType, &f.FixedVersion,
		); err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

func (s *PGStore) ListFindingsByPlatform(ctx context.Context, platformID, scanner string) ([]model.Finding, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.scan_id, f.vuln_id, f.severity, f.package_name, f.package_version,
			COALESCE(f.package_type, ''), COALESCE(f.fixed_version, '')
		FROM findings f
		JOIN scans s ON s.id = f.scan_id
		WHERE s.platform_id = $1 AND s.scanner = $2
		AND s.completed_at = (
			SELECT MAX(s2.completed_at) FROM scans s2
			WHERE s2.platform_id = $1 AND s2.scanner = $2
		)
		ORDER BY f.severity, f.vuln_id, f.package_name
	`, platformID, scanner)
	if err != nil {
		return nil, fmt.Errorf("list findings by platform: %w", err)
	}
	defer rows.Close()

	var findings []model.Finding
	for rows.Next() {
		var f model.Finding
		if err := rows.Scan(
			&f.ScanID, &f.VulnID, &f.Severity, &f.PackageName, &f.PackageVersion,
			&f.PackageType, &f.FixedVersion,
		); err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		findings = append(findings, f)
	}
	return findings, rows.Err()
}

// --- Scanner DB State ---

func (s *PGStore) GetDBState(ctx context.Context, scanner string) (*model.ScannerDBState, error) {
	state := &model.ScannerDBState{}
	err := s.pool.QueryRow(ctx, `
		SELECT scanner, version, checksum, updated_at
		FROM scanner_db_state
		WHERE scanner = $1
	`, scanner).Scan(&state.Scanner, &state.Version, &state.Checksum, &state.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get db state: %w", err)
	}
	return state, nil
}

func (s *PGStore) UpsertDBState(ctx context.Context, state model.ScannerDBState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO scanner_db_state (scanner, version, checksum, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scanner) DO UPDATE SET
			version = EXCLUDED.version,
			checksum = EXCLUDED.checksum,
			updated_at = EXCLUDED.updated_at
	`, state.Scanner, state.Version, state.Checksum, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert db state: %w", err)
	}
	return nil
}
