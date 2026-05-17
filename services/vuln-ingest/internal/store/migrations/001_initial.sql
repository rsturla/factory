CREATE TABLE IF NOT EXISTS vulnerabilities (
    id TEXT PRIMARY KEY,
    aliases TEXT[] DEFAULT '{}',
    summary TEXT,
    details TEXT,
    severity JSONB DEFAULT '[]',
    published TIMESTAMPTZ,
    modified TIMESTAMPTZ,
    withdrawn TIMESTAMPTZ,
    refs JSONB DEFAULT '[]',
    database_specific JSONB,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS affected_packages (
    id BIGSERIAL PRIMARY KEY,
    vuln_id TEXT NOT NULL REFERENCES vulnerabilities(id) ON DELETE CASCADE,
    ecosystem TEXT,
    package_name TEXT,
    purl TEXT,
    version_ranges JSONB DEFAULT '[]',
    versions TEXT[] DEFAULT '{}',
    database_specific JSONB,
    quality_flags TEXT[] DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS source_records (
    vuln_id TEXT NOT NULL REFERENCES vulnerabilities(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    source_id TEXT,
    raw_hash TEXT,
    fetched_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (vuln_id, source)
);

CREATE TABLE IF NOT EXISTS source_checkpoints (
    source TEXT PRIMARY KEY,
    checkpoint_value TEXT NOT NULL,
    last_sync_at TIMESTAMPTZ DEFAULT now(),
    items_synced BIGINT DEFAULT 0,
    error_message TEXT
);
