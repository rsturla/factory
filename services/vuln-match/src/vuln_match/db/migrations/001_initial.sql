CREATE TABLE IF NOT EXISTS advisories (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_package TEXT NOT NULL,
    vuln_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'detected',
    confidence TEXT DEFAULT 'medium',
    match_type TEXT,
    upstream_version TEXT,
    rpm_version TEXT,
    upstream_fixed_version TEXT,
    distro_fixed_version TEXT,
    severity TEXT,
    cvss_score TEXT,
    epss_score REAL,
    in_kev BOOLEAN DEFAULT FALSE,
    flags TEXT[] DEFAULT '{}',
    notes TEXT,
    agent_reasoning TEXT,
    reviewed_by TEXT,
    reviewed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_package, vuln_id)
);

CREATE INDEX IF NOT EXISTS idx_advisories_status ON advisories(status);
CREATE INDEX IF NOT EXISTS idx_advisories_vuln ON advisories(vuln_id);
CREATE INDEX IF NOT EXISTS idx_advisories_package ON advisories(source_package);

CREATE TABLE IF NOT EXISTS name_mappings (
    rpm_name TEXT PRIMARY KEY,
    vuln_names TEXT[] NOT NULL,
    source TEXT NOT NULL DEFAULT 'manual',
    confidence TEXT DEFAULT 'medium',
    reviewed BOOLEAN DEFAULT FALSE,
    agent_reasoning TEXT,
    usage_count INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS match_state (
    source_package TEXT PRIMARY KEY,
    last_matched_at TIMESTAMPTZ,
    vuln_checkpoint TEXT,
    catalog_version TEXT
);
