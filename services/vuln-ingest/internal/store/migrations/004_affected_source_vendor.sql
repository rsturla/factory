ALTER TABLE affected_packages ADD COLUMN IF NOT EXISTS source TEXT;
ALTER TABLE affected_packages ADD COLUMN IF NOT EXISTS vendor TEXT;

CREATE INDEX IF NOT EXISTS idx_affected_source ON affected_packages(source);
CREATE INDEX IF NOT EXISTS idx_affected_vendor_pkg ON affected_packages(vendor, package_name);
