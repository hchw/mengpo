-- Retrieval telemetry: every projection request (cache hit or miss) records
-- what was selected so retrieval quality and cache effectiveness can be
-- evaluated from real traffic rather than only from offline datasets.
ALTER TABLE retrieval_events
    ADD COLUMN IF NOT EXISTS cache_hit boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS tokens_injected integer NOT NULL DEFAULT 0 CHECK (tokens_injected >= 0);

CREATE INDEX IF NOT EXISTS retrieval_events_created_idx
    ON retrieval_events (created_at DESC);
