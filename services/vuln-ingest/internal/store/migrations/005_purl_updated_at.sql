CREATE INDEX IF NOT EXISTS idx_affected_purl ON affected_packages(purl) WHERE purl IS NOT NULL AND purl != '';
CREATE INDEX IF NOT EXISTS idx_vulns_updated_at ON vulnerabilities(updated_at DESC);
