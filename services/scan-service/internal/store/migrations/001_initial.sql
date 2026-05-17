CREATE TABLE IF NOT EXISTS scans (
    id TEXT PRIMARY KEY,
    platform_id TEXT NOT NULL,
    scanner TEXT NOT NULL,
    db_version TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ NOT NULL,
    vuln_count INTEGER NOT NULL DEFAULT 0,
    critical_count INTEGER NOT NULL DEFAULT 0,
    high_count INTEGER NOT NULL DEFAULT 0,
    medium_count INTEGER NOT NULL DEFAULT 0,
    low_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'completed',
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_scans_platform_scanner ON scans(platform_id, scanner, completed_at DESC);
CREATE INDEX IF NOT EXISTS idx_scans_db_version ON scans(scanner, db_version);

CREATE TABLE IF NOT EXISTS findings (
    scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    vuln_id TEXT NOT NULL,
    severity TEXT,
    package_name TEXT NOT NULL,
    package_version TEXT NOT NULL,
    package_type TEXT,
    fixed_version TEXT,
    PRIMARY KEY (scan_id, vuln_id, package_name, package_version)
);
CREATE INDEX IF NOT EXISTS idx_findings_vuln ON findings(vuln_id);
CREATE INDEX IF NOT EXISTS idx_findings_severity ON findings(severity);

CREATE TABLE IF NOT EXISTS scanner_db_state (
    scanner TEXT PRIMARY KEY,
    version TEXT NOT NULL,
    checksum TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
