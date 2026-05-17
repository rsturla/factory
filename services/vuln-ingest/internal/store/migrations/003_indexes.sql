CREATE INDEX IF NOT EXISTS idx_affected_ecosystem_pkg ON affected_packages(ecosystem, package_name);
CREATE INDEX IF NOT EXISTS idx_affected_vuln_id ON affected_packages(vuln_id);
CREATE INDEX IF NOT EXISTS idx_vulns_modified ON vulnerabilities(modified DESC);
CREATE INDEX IF NOT EXISTS idx_vulns_published ON vulnerabilities(published DESC);
CREATE INDEX IF NOT EXISTS idx_source_records_source ON source_records(source);
CREATE INDEX IF NOT EXISTS idx_epss_score ON epss_scores(score DESC);
CREATE INDEX IF NOT EXISTS idx_kev_date_added ON kev_entries(date_added DESC);
CREATE INDEX IF NOT EXISTS idx_vulns_aliases ON vulnerabilities USING gin(aliases);
CREATE INDEX IF NOT EXISTS idx_affected_quality ON affected_packages USING gin(quality_flags);
