DROP INDEX IF EXISTS retrieval_events_created_idx;

ALTER TABLE retrieval_events
    DROP COLUMN IF EXISTS tokens_injected,
    DROP COLUMN IF EXISTS cache_hit;
