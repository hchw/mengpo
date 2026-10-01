DROP INDEX IF EXISTS observed_events_attribution_idx;
ALTER TABLE observed_events
    DROP COLUMN IF EXISTS attribution_level,
    DROP COLUMN IF EXISTS trace_metadata,
    DROP COLUMN IF EXISTS access_level;
