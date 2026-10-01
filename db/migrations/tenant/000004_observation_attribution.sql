ALTER TABLE observed_events
    ADD COLUMN IF NOT EXISTS access_level text NOT NULL DEFAULT 'level0'
        CHECK (access_level IN ('level0', 'level1', 'level2')),
    ADD COLUMN IF NOT EXISTS trace_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS attribution_level text NOT NULL DEFAULT 'unknown'
        CHECK (attribution_level IN ('direct', 'correlated', 'inferred', 'unknown'));

CREATE INDEX IF NOT EXISTS observed_events_attribution_idx
    ON observed_events (attribution_level, ingested_at DESC);
