CREATE TABLE IF NOT EXISTS vendor_notes (
    cve_id TEXT NOT NULL,
    vendor TEXT NOT NULL,
    content JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (cve_id, vendor)
);

CREATE INDEX idx_vendor_notes_vendor ON vendor_notes (vendor);
