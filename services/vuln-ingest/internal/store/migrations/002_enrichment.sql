CREATE TABLE IF NOT EXISTS kev_entries (
    cve_id TEXT PRIMARY KEY,
    vendor_project TEXT,
    product TEXT,
    date_added DATE,
    due_date DATE,
    short_description TEXT,
    required_action TEXT,
    notes TEXT,
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS epss_scores (
    cve_id TEXT PRIMARY KEY,
    score REAL NOT NULL,
    percentile REAL NOT NULL,
    model_version TEXT,
    score_date DATE,
    updated_at TIMESTAMPTZ DEFAULT now()
);
