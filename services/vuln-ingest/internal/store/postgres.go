package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hummingbird-org/vuln-ingest/internal/model"
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

// --- Vulnerabilities ---

func (s *PGStore) UpsertVulnerability(ctx context.Context, v *model.Vulnerability, source string) error {
	severityJSON, err := json.Marshal(v.Severity)
	if err != nil {
		return fmt.Errorf("marshal severity: %w", err)
	}
	refsJSON, err := json.Marshal(v.References)
	if err != nil {
		return fmt.Errorf("marshal references: %w", err)
	}
	dbSpecJSON, err := json.Marshal(v.DatabaseSpecific)
	if err != nil {
		return fmt.Errorf("marshal database_specific: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		INSERT INTO vulnerabilities (id, aliases, summary, details, severity, published, modified, withdrawn, refs, database_specific, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (id) DO UPDATE SET
			aliases = EXCLUDED.aliases,
			summary = EXCLUDED.summary,
			details = EXCLUDED.details,
			severity = EXCLUDED.severity,
			published = EXCLUDED.published,
			modified = EXCLUDED.modified,
			withdrawn = EXCLUDED.withdrawn,
			refs = EXCLUDED.refs,
			database_specific = EXCLUDED.database_specific,
			updated_at = now()
	`, v.ID, v.Aliases, v.Summary, v.Details, severityJSON, v.Published, v.Modified, v.Withdrawn, refsJSON, dbSpecJSON)
	if err != nil {
		return fmt.Errorf("upsert vulnerability: %w", err)
	}

	// Delete only this source's affected packages — preserves entries from other sources.
	if _, err := tx.Exec(ctx, "DELETE FROM affected_packages WHERE vuln_id = $1 AND source = $2", v.ID, source); err != nil {
		return fmt.Errorf("delete affected packages: %w", err)
	}

	for _, ap := range v.AffectedPackages {
		rangesJSON, err := json.Marshal(ap.VersionRanges)
		if err != nil {
			return fmt.Errorf("marshal version_ranges: %w", err)
		}
		dbSpecJSON, err := json.Marshal(ap.DatabaseSpecific)
		if err != nil {
			return fmt.Errorf("marshal affected db_specific: %w", err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO affected_packages (vuln_id, source, vendor, ecosystem, package_name, purl, version_ranges, versions, database_specific, quality_flags)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, v.ID, source, ap.Vendor, ap.Ecosystem, ap.PackageName, ap.Purl, rangesJSON, ap.Versions, dbSpecJSON, ap.QualityFlags)
		if err != nil {
			return fmt.Errorf("insert affected package: %w", err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PGStore) GetVulnerability(ctx context.Context, id string) (*model.Vulnerability, error) {
	v := &model.Vulnerability{}
	var severityJSON, refsJSON, dbSpecJSON []byte

	err := s.pool.QueryRow(ctx, `
		SELECT id, aliases, summary, details, severity, published, modified, withdrawn, refs, database_specific
		FROM vulnerabilities WHERE id = $1
	`, id).Scan(&v.ID, &v.Aliases, &v.Summary, &v.Details, &severityJSON, &v.Published, &v.Modified, &v.Withdrawn, &refsJSON, &dbSpecJSON)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get vulnerability: %w", err)
	}

	if err := json.Unmarshal(severityJSON, &v.Severity); err != nil {
		return nil, fmt.Errorf("unmarshal severity for %s: %w", v.ID, err)
	}
	if err := json.Unmarshal(refsJSON, &v.References); err != nil {
		return nil, fmt.Errorf("unmarshal refs for %s: %w", v.ID, err)
	}
	if err := json.Unmarshal(dbSpecJSON, &v.DatabaseSpecific); err != nil {
		return nil, fmt.Errorf("unmarshal database_specific for %s: %w", v.ID, err)
	}

	affected, err := s.getAffectedPackages(ctx, id)
	if err != nil {
		return nil, err
	}
	v.AffectedPackages = affected

	return v, nil
}

func (s *PGStore) GetRelatedVulnerabilities(ctx context.Context, id string) ([]*model.Vulnerability, error) {
	// Find all vuln IDs linked by alias: vulns whose aliases contain this ID,
	// plus vulns whose ID appears in this vuln's aliases.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT id FROM (
			SELECT id FROM vulnerabilities WHERE $1 = ANY(aliases)
			UNION
			SELECT unnest(aliases) AS id FROM vulnerabilities WHERE id = $1
		) sub
		WHERE id != $1
		AND EXISTS (SELECT 1 FROM vulnerabilities WHERE vulnerabilities.id = sub.id)
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get related: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var relID string
		if err := rows.Scan(&relID); err != nil {
			return nil, err
		}
		ids = append(ids, relID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return s.BatchGetVulnerabilities(ctx, ids)
}

func (s *PGStore) getAffectedPackages(ctx context.Context, vulnID string) ([]model.AffectedPackage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source, vendor, ecosystem, package_name, purl, version_ranges, versions, database_specific, quality_flags
		FROM affected_packages WHERE vuln_id = $1
	`, vulnID)
	if err != nil {
		return nil, fmt.Errorf("query affected packages: %w", err)
	}
	defer rows.Close()

	var result []model.AffectedPackage
	for rows.Next() {
		var ap model.AffectedPackage
		var rangesJSON, dbSpecJSON []byte
		if err := rows.Scan(&ap.Source, &ap.Vendor, &ap.Ecosystem, &ap.PackageName, &ap.Purl, &rangesJSON, &ap.Versions, &dbSpecJSON, &ap.QualityFlags); err != nil {
			return nil, fmt.Errorf("scan affected package: %w", err)
		}
		if err := json.Unmarshal(rangesJSON, &ap.VersionRanges); err != nil {
			return nil, fmt.Errorf("unmarshal version_ranges for %s: %w", vulnID, err)
		}
		if err := json.Unmarshal(dbSpecJSON, &ap.DatabaseSpecific); err != nil {
			return nil, fmt.Errorf("unmarshal affected database_specific for %s: %w", vulnID, err)
		}
		result = append(result, ap)
	}
	return result, rows.Err()
}

func (s *PGStore) ListVulnerabilities(ctx context.Context, opts ListOpts) ([]*model.Vulnerability, error) {
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	var args []any
	query := "SELECT id FROM vulnerabilities"
	argIdx := 1
	var conditions []string

	if opts.ModifiedSince != nil {
		conditions = append(conditions, fmt.Sprintf("modified >= $%d", argIdx))
		args = append(args, *opts.ModifiedSince)
		argIdx++
	}
	if opts.UpdatedSince != nil {
		conditions = append(conditions, fmt.Sprintf("updated_at >= $%d", argIdx))
		args = append(args, *opts.UpdatedSince)
		argIdx++
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY updated_at DESC NULLS LAST"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, opts.Limit, opts.Offset)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list vulnerabilities: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return s.BatchGetVulnerabilities(ctx, ids)
}

func (s *PGStore) BatchGetVulnerabilities(ctx context.Context, ids []string) ([]*model.Vulnerability, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, aliases, summary, details, severity, published, modified, withdrawn, refs, database_specific
		FROM vulnerabilities WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("batch get vulns: %w", err)
	}
	defer rows.Close()

	vulnMap := make(map[string]*model.Vulnerability, len(ids))
	for rows.Next() {
		v := &model.Vulnerability{}
		var severityJSON, refsJSON, dbSpecJSON []byte
		if err := rows.Scan(&v.ID, &v.Aliases, &v.Summary, &v.Details, &severityJSON, &v.Published, &v.Modified, &v.Withdrawn, &refsJSON, &dbSpecJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(severityJSON, &v.Severity); err != nil {
			return nil, fmt.Errorf("unmarshal severity for %s: %w", v.ID, err)
		}
		if err := json.Unmarshal(refsJSON, &v.References); err != nil {
			return nil, fmt.Errorf("unmarshal refs for %s: %w", v.ID, err)
		}
		if err := json.Unmarshal(dbSpecJSON, &v.DatabaseSpecific); err != nil {
			return nil, fmt.Errorf("unmarshal database_specific for %s: %w", v.ID, err)
		}
		vulnMap[v.ID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(vulnMap) == 0 {
		return nil, nil
	}

	// Batch fetch affected packages.
	var vulnIDs []string
	for id := range vulnMap {
		vulnIDs = append(vulnIDs, id)
	}

	apRows, err := s.pool.Query(ctx, `
		SELECT vuln_id, source, vendor, ecosystem, package_name, purl, version_ranges, versions, database_specific, quality_flags
		FROM affected_packages WHERE vuln_id = ANY($1)
	`, vulnIDs)
	if err != nil {
		return nil, fmt.Errorf("batch get affected: %w", err)
	}
	defer apRows.Close()

	for apRows.Next() {
		var vulnID string
		var ap model.AffectedPackage
		var rangesJSON, dbSpecJSON []byte
		if err := apRows.Scan(&vulnID, &ap.Source, &ap.Vendor, &ap.Ecosystem, &ap.PackageName, &ap.Purl, &rangesJSON, &ap.Versions, &dbSpecJSON, &ap.QualityFlags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(rangesJSON, &ap.VersionRanges); err != nil {
			return nil, fmt.Errorf("unmarshal version_ranges for %s: %w", vulnID, err)
		}
		if err := json.Unmarshal(dbSpecJSON, &ap.DatabaseSpecific); err != nil {
			return nil, fmt.Errorf("unmarshal affected database_specific for %s: %w", vulnID, err)
		}

		if v, ok := vulnMap[vulnID]; ok {
			v.AffectedPackages = append(v.AffectedPackages, ap)
		}
	}
	if err := apRows.Err(); err != nil {
		return nil, err
	}

	// Preserve input order.
	var result []*model.Vulnerability
	for _, id := range ids {
		if v, ok := vulnMap[id]; ok {
			result = append(result, v)
		}
	}
	return result, nil
}

func (s *PGStore) ListAffectedByPackage(ctx context.Context, ecosystem, packageName string, opts ListOpts) ([]*model.Vulnerability, error) {
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	var query string
	var args []any
	if ecosystem != "" {
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE ecosystem = $1 AND package_name = $2 ORDER BY vuln_id LIMIT $3 OFFSET $4"
		args = []any{ecosystem, packageName, opts.Limit, opts.Offset}
	} else {
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE package_name = $1 ORDER BY vuln_id LIMIT $2 OFFSET $3"
		args = []any{packageName, opts.Limit, opts.Offset}
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list affected by package: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return s.BatchGetVulnerabilities(ctx, ids)
}

func (s *PGStore) ListAffectedByPurl(ctx context.Context, purl string, opts ListOpts) ([]*model.Vulnerability, error) {
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT vuln_id FROM affected_packages
		WHERE purl = $1
		ORDER BY vuln_id
		LIMIT $2 OFFSET $3
	`, purl, opts.Limit, opts.Offset)
	if err != nil {
		return nil, fmt.Errorf("list affected by purl: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return s.BatchGetVulnerabilities(ctx, ids)
}

func (s *PGStore) BatchQueryAffected(ctx context.Context, queries []AffectedQuery, opts ListOpts) (map[string][]*model.Vulnerability, error) {
	if opts.Limit == 0 {
		opts.Limit = 100
	}

	result := make(map[string][]*model.Vulnerability, len(queries))

	for _, q := range queries {
		key := q.Ecosystem + ":" + q.PackageName
		if q.Vendor != "" {
			key = q.Vendor + "/" + q.PackageName
		}
		if q.Purl != "" {
			key = q.Purl
		}

		ids, err := s.queryAffectedIDs(ctx, q, opts.Limit)
		if err != nil {
			return nil, fmt.Errorf("batch query %s: %w", key, err)
		}

		if len(ids) > 0 {
			vulns, err := s.BatchGetVulnerabilities(ctx, ids)
			if err != nil {
				return nil, err
			}
			result[key] = vulns
		}
	}

	return result, nil
}

func (s *PGStore) queryAffectedIDs(ctx context.Context, q AffectedQuery, limit int) ([]string, error) {
	var query string
	var args []any

	switch {
	case q.Purl != "":
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE purl = $1 LIMIT $2"
		args = []any{q.Purl, limit}
	case q.Vendor != "" && q.PackageName != "":
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE vendor = $1 AND package_name = $2 LIMIT $3"
		args = []any{q.Vendor, q.PackageName, limit}
	case q.Ecosystem != "" && q.PackageName != "":
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE ecosystem = $1 AND package_name = $2 LIMIT $3"
		args = []any{q.Ecosystem, q.PackageName, limit}
	case q.PackageName != "":
		query = "SELECT DISTINCT vuln_id FROM affected_packages WHERE package_name = $1 LIMIT $2"
		args = []any{q.PackageName, limit}
	default:
		return nil, nil
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *PGStore) CountVulnerabilities(ctx context.Context, opts ListOpts) (int, error) {
	query := "SELECT COUNT(*) FROM vulnerabilities"
	var args []any
	argIdx := 1
	var conditions []string

	if opts.ModifiedSince != nil {
		conditions = append(conditions, fmt.Sprintf("modified >= $%d", argIdx))
		args = append(args, *opts.ModifiedSince)
		argIdx++
	}
	if opts.UpdatedSince != nil {
		conditions = append(conditions, fmt.Sprintf("updated_at >= $%d", argIdx))
		args = append(args, *opts.UpdatedSince)
		argIdx++
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	var count int
	err := s.pool.QueryRow(ctx, query, args...).Scan(&count)
	return count, err
}

func (s *PGStore) CountAffectedByPackage(ctx context.Context, ecosystem, packageName string) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT vuln_id) FROM affected_packages
		WHERE ecosystem = $1 AND package_name = $2
	`, ecosystem, packageName).Scan(&count)
	return count, err
}

// --- Source Records ---

func (s *PGStore) UpsertSourceRecord(ctx context.Context, rec *model.SourceRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_records (vuln_id, source, source_id, raw_hash, fetched_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (vuln_id, source) DO UPDATE SET
			source_id = EXCLUDED.source_id,
			raw_hash = EXCLUDED.raw_hash,
			fetched_at = now()
	`, rec.VulnID, rec.Source, rec.SourceID, rec.RawHash)
	return err
}

func (s *PGStore) GetSourceRecord(ctx context.Context, vulnID, source string) (*model.SourceRecord, error) {
	rec := &model.SourceRecord{}
	err := s.pool.QueryRow(ctx, `
		SELECT vuln_id, source, source_id, raw_hash, fetched_at
		FROM source_records WHERE vuln_id = $1 AND source = $2
	`, vulnID, source).Scan(&rec.VulnID, &rec.Source, &rec.SourceID, &rec.RawHash, &rec.FetchedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return rec, err
}

// --- Checkpoints ---

func (s *PGStore) GetCheckpoint(ctx context.Context, source string) (*model.SourceCheckpoint, error) {
	cp := &model.SourceCheckpoint{}
	err := s.pool.QueryRow(ctx, `
		SELECT source, checkpoint_value, last_sync_at, items_synced, COALESCE(error_message, '')
		FROM source_checkpoints WHERE source = $1
	`, source).Scan(&cp.Source, &cp.CheckpointValue, &cp.LastSyncAt, &cp.ItemsSynced, &cp.ErrorMessage)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return cp, err
}

func (s *PGStore) UpdateCheckpoint(ctx context.Context, source, checkpointValue string, itemsSynced int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_checkpoints (source, checkpoint_value, last_sync_at, items_synced, error_message)
		VALUES ($1, $2, now(), $3, NULL)
		ON CONFLICT (source) DO UPDATE SET
			checkpoint_value = EXCLUDED.checkpoint_value,
			last_sync_at = now(),
			items_synced = source_checkpoints.items_synced + EXCLUDED.items_synced,
			error_message = NULL
	`, source, checkpointValue, itemsSynced)
	return err
}

func (s *PGStore) SetCheckpointError(ctx context.Context, source, errMsg string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_checkpoints (source, checkpoint_value, error_message)
		VALUES ($1, '', $2)
		ON CONFLICT (source) DO UPDATE SET error_message = EXCLUDED.error_message
	`, source, errMsg)
	return err
}

func (s *PGStore) ListCheckpoints(ctx context.Context) ([]*model.SourceCheckpoint, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source, checkpoint_value, last_sync_at, items_synced, COALESCE(error_message, '')
		FROM source_checkpoints ORDER BY source
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*model.SourceCheckpoint
	for rows.Next() {
		cp := &model.SourceCheckpoint{}
		if err := rows.Scan(&cp.Source, &cp.CheckpointValue, &cp.LastSyncAt, &cp.ItemsSynced, &cp.ErrorMessage); err != nil {
			return nil, err
		}
		result = append(result, cp)
	}
	return result, rows.Err()
}

// --- Enrichment ---

func (s *PGStore) UpsertKEVEntries(ctx context.Context, entries []model.KEVEntry) error {
	if len(entries) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, e := range entries {
		_, err := tx.Exec(ctx, `
			INSERT INTO kev_entries (cve_id, vendor_project, product, date_added, due_date, short_description, required_action, notes, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
			ON CONFLICT (cve_id) DO UPDATE SET
				vendor_project = EXCLUDED.vendor_project,
				product = EXCLUDED.product,
				date_added = EXCLUDED.date_added,
				due_date = EXCLUDED.due_date,
				short_description = EXCLUDED.short_description,
				required_action = EXCLUDED.required_action,
				notes = EXCLUDED.notes,
				updated_at = now()
		`, e.CVEID, e.VendorProject, e.Product, e.DateAdded, e.DueDate, e.ShortDescription, e.RequiredAction, e.Notes)
		if err != nil {
			return fmt.Errorf("upsert kev entry %s: %w", e.CVEID, err)
		}
	}

	return tx.Commit(ctx)
}

func (s *PGStore) GetKEVEntry(ctx context.Context, cveID string) (*model.KEVEntry, error) {
	e := &model.KEVEntry{}
	err := s.pool.QueryRow(ctx, `
		SELECT cve_id, vendor_project, product, date_added, due_date, short_description, required_action, notes
		FROM kev_entries WHERE cve_id = $1
	`, cveID).Scan(&e.CVEID, &e.VendorProject, &e.Product, &e.DateAdded, &e.DueDate, &e.ShortDescription, &e.RequiredAction, &e.Notes)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return e, err
}


func (s *PGStore) UpsertEPSSScores(ctx context.Context, scores []model.EPSSScore) error {
	if len(scores) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	batch := &pgx.Batch{}
	for _, sc := range scores {
		batch.Queue(`
			INSERT INTO epss_scores (cve_id, score, percentile, model_version, score_date, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (cve_id) DO UPDATE SET
				score = EXCLUDED.score,
				percentile = EXCLUDED.percentile,
				model_version = EXCLUDED.model_version,
				score_date = EXCLUDED.score_date,
				updated_at = now()
		`, sc.CVEID, sc.Score, sc.Percentile, sc.ModelVersion, sc.ScoreDate)
	}

	br := tx.SendBatch(ctx, batch)
	for range scores {
		if _, err := br.Exec(); err != nil {
			br.Close() //nolint:errcheck // already returning an error
			return fmt.Errorf("upsert epss score: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("close batch: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PGStore) GetEPSSScore(ctx context.Context, cveID string) (*model.EPSSScore, error) {
	sc := &model.EPSSScore{}
	err := s.pool.QueryRow(ctx, `
		SELECT cve_id, score, percentile, model_version, score_date
		FROM epss_scores WHERE cve_id = $1
	`, cveID).Scan(&sc.CVEID, &sc.Score, &sc.Percentile, &sc.ModelVersion, &sc.ScoreDate)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return sc, err
}

// --- Helpers for enrichment diffing ---

func (s *PGStore) GetAllEPSSScoreMap(ctx context.Context) (map[string]float32, error) {
	rows, err := s.pool.Query(ctx, "SELECT cve_id, score FROM epss_scores")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]float32)
	for rows.Next() {
		var id string
		var score float32
		if err := rows.Scan(&id, &score); err != nil {
			return nil, err
		}
		m[id] = score
	}
	return m, rows.Err()
}

func (s *PGStore) GetAllKEVIDs(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, "SELECT cve_id, updated_at FROM kev_entries")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var updatedAt time.Time
		if err := rows.Scan(&id, &updatedAt); err != nil {
			return nil, err
		}
		m[id] = updatedAt
	}
	return m, rows.Err()
}
