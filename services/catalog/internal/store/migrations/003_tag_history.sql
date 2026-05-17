ALTER TABLE image_tags DROP CONSTRAINT IF EXISTS image_tags_pkey;
ALTER TABLE image_tags ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ DEFAULT now();
ALTER TABLE image_tags ADD COLUMN IF NOT EXISTS current BOOLEAN DEFAULT true;
CREATE UNIQUE INDEX IF NOT EXISTS idx_image_tags_current ON image_tags (registry, repository, tag) WHERE current = true;
