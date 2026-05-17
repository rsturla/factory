CREATE TABLE IF NOT EXISTS images (
    id TEXT PRIMARY KEY,
    digest TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS image_tags (
    image_id TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    registry TEXT NOT NULL,
    repository TEXT NOT NULL,
    tag TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (image_id, registry, repository, tag)
);

CREATE TABLE IF NOT EXISTS platforms (
    id TEXT PRIMARY KEY,
    image_id TEXT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    os TEXT NOT NULL DEFAULT 'linux',
    architecture TEXT NOT NULL,
    variant TEXT DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS packages (
    id TEXT PRIMARY KEY,
    purl TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    namespace TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS platform_packages (
    platform_id TEXT NOT NULL REFERENCES platforms(id) ON DELETE CASCADE,
    package_id TEXT NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    PRIMARY KEY (platform_id, package_id)
);

CREATE TABLE IF NOT EXISTS sboms (
    id TEXT PRIMARY KEY,
    platform_id TEXT NOT NULL REFERENCES platforms(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    format TEXT NOT NULL DEFAULT 'spdx-json',
    content_hash TEXT NOT NULL,
    raw BYTEA,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE (platform_id, source)
);

CREATE TABLE IF NOT EXISTS discover_checkpoints (
    source TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_image_tags_repo ON image_tags (registry, repository);
CREATE INDEX IF NOT EXISTS idx_image_tags_image ON image_tags (image_id);
CREATE INDEX IF NOT EXISTS idx_platforms_image_id ON platforms (image_id);
CREATE INDEX IF NOT EXISTS idx_packages_name ON packages (name);
CREATE INDEX IF NOT EXISTS idx_packages_type_name ON packages (type, name);
CREATE INDEX IF NOT EXISTS idx_platform_packages_package_id ON platform_packages (package_id);
CREATE INDEX IF NOT EXISTS idx_sboms_platform_source ON sboms (platform_id, source);
