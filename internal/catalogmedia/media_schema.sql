CREATE TABLE IF NOT EXISTS catalog_media (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title text NOT NULL,
    description text NOT NULL,
    media_type text NOT NULL CHECK (media_type IN ('video', 'audio')),
    status text NOT NULL CHECK (status IN ('processing', 'ready', 'failed', 'published')),
    duration_seconds integer NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    cover_type text NOT NULL DEFAULT '' CHECK (cover_type IN ('', 'image/jpeg', 'image/png')),
    created_at timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    claim_id uuid,
    lease_until timestamptz,
    CHECK ((claim_id IS NULL) = (lease_until IS NULL))
);

CREATE INDEX IF NOT EXISTS catalog_media_processing_idx ON catalog_media (created_at) WHERE status = 'processing';

CREATE OR REPLACE VIEW catalog_published_media WITH (security_barrier = true) AS
SELECT id, title, description, media_type, status, duration_seconds, cover_type, created_at
FROM catalog_media
WHERE status = 'published';
