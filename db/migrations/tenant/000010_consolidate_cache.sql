-- Consolidate analysis-result cache. Replaces the external key/value store so
-- identical analysis inputs can reuse a prior result without a second model
-- call. The table lives inside each tenant schema, so isolation between tenants
-- is structural in addition to the tenant-scoped cache key.
CREATE TABLE IF NOT EXISTS consolidate_cache (
    cache_key  text PRIMARY KEY,
    payload    jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS consolidate_cache_expiration_idx ON consolidate_cache (expires_at);
