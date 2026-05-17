package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rsturla/factory/services/catalog/internal/model"
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

// --- Images ---

func (s *PGStore) UpsertImage(ctx context.Context, img model.Image) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO images (id, digest, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			digest = EXCLUDED.digest,
			updated_at = now()
	`, img.ID, img.Digest)
	if err != nil {
		return fmt.Errorf("upsert image: %w", err)
	}
	return nil
}

func (s *PGStore) GetImage(ctx context.Context, id string) (*model.Image, error) {
	img := &model.Image{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, digest FROM images WHERE id = $1
	`, id).Scan(&img.ID, &img.Digest)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get image: %w", err)
	}

	platforms, err := s.ListPlatformsByImage(ctx, id)
	if err != nil {
		return nil, err
	}
	img.Platforms = platforms

	tags, err := s.ListTagsByImage(ctx, id)
	if err != nil {
		return nil, err
	}
	img.Tags = tags

	return img, nil
}

func (s *PGStore) GetImageByDigest(ctx context.Context, digest string) (*model.Image, error) {
	img := &model.Image{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, digest FROM images WHERE digest = $1
	`, digest).Scan(&img.ID, &img.Digest)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get image by digest: %w", err)
	}

	platforms, err := s.ListPlatformsByImage(ctx, img.ID)
	if err != nil {
		return nil, err
	}
	img.Platforms = platforms

	tags, err := s.ListTagsByImage(ctx, img.ID)
	if err != nil {
		return nil, err
	}
	img.Tags = tags

	return img, nil
}

func (s *PGStore) ListImages(ctx context.Context, limit, offset int) ([]model.Image, int, error) {
	if limit <= 0 {
		limit = 100
	}

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM images`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count images: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, digest FROM images
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	var images []model.Image
	for rows.Next() {
		var img model.Image
		if err := rows.Scan(&img.ID, &img.Digest); err != nil {
			return nil, 0, fmt.Errorf("scan image: %w", err)
		}
		images = append(images, img)
	}
	return images, total, rows.Err()
}

// --- Tags ---

func (s *PGStore) UpsertTag(ctx context.Context, imageID string, tag model.Tag) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin upsert tag: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Mark any existing current row for this (registry, repository, tag) as historical.
	_, err = tx.Exec(ctx, `
		UPDATE image_tags SET current = false, updated_at = now()
		WHERE registry = $1 AND repository = $2 AND tag = $3
			AND current = true AND image_id != $4
	`, tag.Registry, tag.Repository, tag.Tag, imageID)
	if err != nil {
		return fmt.Errorf("retire old tag: %w", err)
	}

	// Insert new current row (or no-op if this exact mapping already exists and is current).
	_, err = tx.Exec(ctx, `
		INSERT INTO image_tags (image_id, registry, repository, tag, current, updated_at)
		VALUES ($1, $2, $3, $4, true, now())
		ON CONFLICT (registry, repository, tag) WHERE current = true
		DO UPDATE SET updated_at = now()
	`, imageID, tag.Registry, tag.Repository, tag.Tag)
	if err != nil {
		return fmt.Errorf("upsert tag: %w", err)
	}

	return tx.Commit(ctx)
}

func (s *PGStore) ListTagsByImage(ctx context.Context, imageID string) ([]model.Tag, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT registry, repository, tag FROM image_tags
		WHERE image_id = $1 AND current = true
		ORDER BY registry, repository, tag
	`, imageID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var tags []model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.Registry, &t.Repository, &t.Tag); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (s *PGStore) GetImageByTag(ctx context.Context, registry, repository, tag string) (*model.Image, error) {
	var imageID string
	err := s.pool.QueryRow(ctx, `
		SELECT image_id FROM image_tags
		WHERE registry = $1 AND repository = $2 AND tag = $3 AND current = true
	`, registry, repository, tag).Scan(&imageID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get image by tag: %w", err)
	}
	return s.GetImage(ctx, imageID)
}

// --- Platforms ---

func (s *PGStore) UpsertPlatform(ctx context.Context, p model.Platform) error {
	var configJSON []byte
	if p.Config != nil {
		var err error
		configJSON, err = json.Marshal(p.Config)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO platforms (id, image_id, os, architecture, variant, config, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (id) DO UPDATE SET
			image_id = EXCLUDED.image_id,
			os = EXCLUDED.os,
			architecture = EXCLUDED.architecture,
			variant = EXCLUDED.variant,
			config = COALESCE(EXCLUDED.config, platforms.config),
			updated_at = now()
	`, p.ID, p.ImageID, p.OS, p.Architecture, p.Variant, configJSON)
	if err != nil {
		return fmt.Errorf("upsert platform: %w", err)
	}
	return nil
}

func (s *PGStore) GetPlatform(ctx context.Context, id string) (*model.Platform, error) {
	p := &model.Platform{}
	var configJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, image_id, os, architecture, variant, config FROM platforms WHERE id = $1
	`, id).Scan(&p.ID, &p.ImageID, &p.OS, &p.Architecture, &p.Variant, &configJSON)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get platform: %w", err)
	}
	if configJSON != nil {
		p.Config = &model.PlatformConfig{}
		if err := json.Unmarshal(configJSON, p.Config); err != nil {
			return nil, fmt.Errorf("unmarshal config: %w", err)
		}
	}
	return p, nil
}

func (s *PGStore) ListPlatformsByImage(ctx context.Context, imageID string) ([]model.Platform, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, image_id, os, architecture, variant, config FROM platforms
		WHERE image_id = $1
		ORDER BY architecture, variant
	`, imageID)
	if err != nil {
		return nil, fmt.Errorf("list platforms: %w", err)
	}
	defer rows.Close()

	var platforms []model.Platform
	for rows.Next() {
		var p model.Platform
		var configJSON []byte
		if err := rows.Scan(&p.ID, &p.ImageID, &p.OS, &p.Architecture, &p.Variant, &configJSON); err != nil {
			return nil, fmt.Errorf("scan platform: %w", err)
		}
		if configJSON != nil {
			p.Config = &model.PlatformConfig{}
			json.Unmarshal(configJSON, p.Config)
		}
		platforms = append(platforms, p)
	}
	return platforms, rows.Err()
}

// --- Packages ---

func (s *PGStore) UpsertPackage(ctx context.Context, pkg model.Package) (string, error) {
	if pkg.ID == "" {
		pkg.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(pkg.PURL)))[:16]
	}

	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO packages (id, purl, type, name, version, namespace)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (purl) DO UPDATE SET
			type = EXCLUDED.type,
			name = EXCLUDED.name,
			version = EXCLUDED.version,
			namespace = EXCLUDED.namespace
		RETURNING id
	`, pkg.ID, pkg.PURL, pkg.Type, pkg.Name, pkg.Version, pkg.Namespace).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert package: %w", err)
	}
	return id, nil
}

func (s *PGStore) AssociatePackages(ctx context.Context, platformID string, packageIDs []string) error {
	if len(packageIDs) == 0 {
		return nil
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, pkgID := range packageIDs {
		batch.Queue(`
			INSERT INTO platform_packages (platform_id, package_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, platformID, pkgID)
	}

	br := tx.SendBatch(ctx, batch)
	for range packageIDs {
		if _, err := br.Exec(); err != nil {
			br.Close()
			return fmt.Errorf("associate package: %w", err)
		}
	}
	br.Close()

	return tx.Commit(ctx)
}

func (s *PGStore) ListPackagesByPlatform(ctx context.Context, platformID string) ([]model.Package, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.purl, p.type, p.name, p.version, p.namespace
		FROM packages p
		JOIN platform_packages pp ON pp.package_id = p.id
		WHERE pp.platform_id = $1
		ORDER BY p.type, p.name, p.version
	`, platformID)
	if err != nil {
		return nil, fmt.Errorf("list packages by platform: %w", err)
	}
	defer rows.Close()

	var packages []model.Package
	for rows.Next() {
		var pkg model.Package
		if err := rows.Scan(&pkg.ID, &pkg.PURL, &pkg.Type, &pkg.Name, &pkg.Version, &pkg.Namespace); err != nil {
			return nil, fmt.Errorf("scan package: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, rows.Err()
}

func (s *PGStore) SearchPackages(ctx context.Context, name string, limit int) ([]model.Package, error) {
	if limit <= 0 {
		limit = 100
	}

	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(name)

	rows, err := s.pool.Query(ctx, `
		SELECT id, purl, type, name, version, namespace
		FROM packages
		WHERE name ILIKE $1 ESCAPE '\'
		ORDER BY name, version
		LIMIT $2
	`, "%"+escaped+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("search packages: %w", err)
	}
	defer rows.Close()

	var packages []model.Package
	for rows.Next() {
		var pkg model.Package
		if err := rows.Scan(&pkg.ID, &pkg.PURL, &pkg.Type, &pkg.Name, &pkg.Version, &pkg.Namespace); err != nil {
			return nil, fmt.Errorf("scan package: %w", err)
		}
		packages = append(packages, pkg)
	}
	return packages, rows.Err()
}

func (s *PGStore) GetImagesByPackage(ctx context.Context, purl string) ([]model.Image, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT i.id, i.digest
		FROM images i
		JOIN platforms pl ON pl.image_id = i.id
		JOIN platform_packages pp ON pp.platform_id = pl.id
		JOIN packages p ON p.id = pp.package_id
		WHERE p.purl = $1
		ORDER BY i.digest
	`, purl)
	if err != nil {
		return nil, fmt.Errorf("get images by package: %w", err)
	}
	defer rows.Close()

	var images []model.Image
	for rows.Next() {
		var img model.Image
		if err := rows.Scan(&img.ID, &img.Digest); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

func (s *PGStore) GetImagesByPackageName(ctx context.Context, name, version string, limit int) ([]model.Image, error) {
	if limit <= 0 {
		limit = 100
	}

	esc := strings.NewReplacer("%", "\\%", "_", "\\_")

	if version != "" {
		rows, err := s.pool.Query(ctx, `
			SELECT DISTINCT i.id, i.digest
			FROM images i
			JOIN platforms pl ON pl.image_id = i.id
			JOIN platform_packages pp ON pp.platform_id = pl.id
			JOIN packages p ON p.id = pp.package_id
			WHERE p.name ILIKE $1 ESCAPE '\' AND p.version ILIKE $2 ESCAPE '\'
			ORDER BY i.digest
			LIMIT $3
		`, "%"+esc.Replace(name)+"%", "%"+esc.Replace(version)+"%", limit)
		if err != nil {
			return nil, fmt.Errorf("get images by package name+version: %w", err)
		}
		defer rows.Close()
		return scanImages(rows)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT i.id, i.digest
		FROM images i
		JOIN platforms pl ON pl.image_id = i.id
		JOIN platform_packages pp ON pp.platform_id = pl.id
		JOIN packages p ON p.id = pp.package_id
		WHERE p.name ILIKE $1 ESCAPE '\'
		ORDER BY i.digest
		LIMIT $2
	`, "%"+esc.Replace(name)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("get images by package name: %w", err)
	}
	defer rows.Close()
	return scanImages(rows)
}

func (s *PGStore) DiffPackages(ctx context.Context, fromPlatformID, toPlatformID string) (added []model.Package, removed []model.Package, err error) {
	// Added: packages in 'to' but not in 'from'
	addedRows, err := s.pool.Query(ctx, `
		SELECT p.id, p.purl, p.type, p.name, p.version, p.namespace
		FROM packages p
		JOIN platform_packages pp ON pp.package_id = p.id
		WHERE pp.platform_id = $2
		AND p.id NOT IN (SELECT package_id FROM platform_packages WHERE platform_id = $1)
		ORDER BY p.type, p.name, p.version
	`, fromPlatformID, toPlatformID)
	if err != nil {
		return nil, nil, fmt.Errorf("diff added packages: %w", err)
	}
	defer addedRows.Close()

	for addedRows.Next() {
		var pkg model.Package
		if err := addedRows.Scan(&pkg.ID, &pkg.PURL, &pkg.Type, &pkg.Name, &pkg.Version, &pkg.Namespace); err != nil {
			return nil, nil, fmt.Errorf("scan added package: %w", err)
		}
		added = append(added, pkg)
	}
	if err := addedRows.Err(); err != nil {
		return nil, nil, err
	}

	// Removed: packages in 'from' but not in 'to'
	removedRows, err := s.pool.Query(ctx, `
		SELECT p.id, p.purl, p.type, p.name, p.version, p.namespace
		FROM packages p
		JOIN platform_packages pp ON pp.package_id = p.id
		WHERE pp.platform_id = $1
		AND p.id NOT IN (SELECT package_id FROM platform_packages WHERE platform_id = $2)
		ORDER BY p.type, p.name, p.version
	`, fromPlatformID, toPlatformID)
	if err != nil {
		return nil, nil, fmt.Errorf("diff removed packages: %w", err)
	}
	defer removedRows.Close()

	for removedRows.Next() {
		var pkg model.Package
		if err := removedRows.Scan(&pkg.ID, &pkg.PURL, &pkg.Type, &pkg.Name, &pkg.Version, &pkg.Namespace); err != nil {
			return nil, nil, fmt.Errorf("scan removed package: %w", err)
		}
		removed = append(removed, pkg)
	}
	if err := removedRows.Err(); err != nil {
		return nil, nil, err
	}

	return added, removed, nil
}

func scanImages(rows pgx.Rows) ([]model.Image, error) {
	var images []model.Image
	for rows.Next() {
		var img model.Image
		if err := rows.Scan(&img.ID, &img.Digest); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// --- SBOMs ---

func (s *PGStore) UpsertSBOM(ctx context.Context, sbom model.SBOM) error {
	if sbom.ID == "" {
		sbom.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(sbom.PlatformID+"|"+sbom.Source)))[:16]
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO sboms (id, platform_id, source, format, content_hash, raw, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (platform_id, source) DO UPDATE SET
			format = EXCLUDED.format,
			content_hash = EXCLUDED.content_hash,
			raw = EXCLUDED.raw,
			updated_at = now()
	`, sbom.ID, sbom.PlatformID, sbom.Source, sbom.Format, sbom.ContentHash, sbom.Raw)
	if err != nil {
		return fmt.Errorf("upsert sbom: %w", err)
	}
	return nil
}

func (s *PGStore) GetSBOM(ctx context.Context, platformID, source string) (*model.SBOM, error) {
	sbom := &model.SBOM{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, platform_id, source, format, content_hash, raw
		FROM sboms WHERE platform_id = $1 AND source = $2
	`, platformID, source).Scan(&sbom.ID, &sbom.PlatformID, &sbom.Source, &sbom.Format, &sbom.ContentHash, &sbom.Raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sbom: %w", err)
	}
	return sbom, nil
}

// --- Checkpoints ---

func (s *PGStore) GetCheckpoint(ctx context.Context, source string) (string, error) {
	var value string
	err := s.pool.QueryRow(ctx, `
		SELECT value FROM discover_checkpoints WHERE source = $1
	`, source).Scan(&value)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get checkpoint: %w", err)
	}
	return value, nil
}

func (s *PGStore) UpdateCheckpoint(ctx context.Context, source, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO discover_checkpoints (source, value, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (source) DO UPDATE SET
			value = EXCLUDED.value,
			updated_at = now()
	`, source, value)
	if err != nil {
		return fmt.Errorf("update checkpoint: %w", err)
	}
	return nil
}
