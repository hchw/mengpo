-- Analysis run records (audit), maintenance cursors, and per-tenant provider
-- configuration with an encrypted secret. All objects live in the tenant schema
-- so they are isolated per tenant and dropped with the tenant.

ALTER TABLE analysis_jobs
    ADD COLUMN IF NOT EXISTS trigger_source text NOT NULL DEFAULT 'event',
    ADD COLUMN IF NOT EXISTS priority smallint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tokens_prompt integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS tokens_completion integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS latency_ms bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cost_usd double precision NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS candidate_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS discarded_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS conflict_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS degraded_reason text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS input_event_ids jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE INDEX IF NOT EXISTS analysis_jobs_task_status_created_idx ON analysis_jobs (task_type, status, created_at DESC);
CREATE INDEX IF NOT EXISTS analysis_jobs_session_created_idx ON analysis_jobs (session_id, created_at DESC);

-- Maintenance cursors let the periodic maintenance loop resume without
-- reprocessing or duplicating work.
CREATE TABLE IF NOT EXISTS maintenance_cursors (
    task_type text PRIMARY KEY,
    watermark bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Per-tenant provider configuration. The API key is stored as AES-256-GCM
-- ciphertext; the plaintext never reaches the database.
CREATE TABLE IF NOT EXISTS provider_configs (
    provider text PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT false,
    base_url text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    settings jsonb NOT NULL DEFAULT '{}'::jsonb,
    secret_ciphertext bytea,
    key_version integer NOT NULL DEFAULT 0,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now()
);
