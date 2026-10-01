-- Link analysis runs to the memories they produced, and decouple runs from the
-- durable outbox so periodic scans can record runs without a synthetic job.
ALTER TABLE analysis_jobs
    ADD COLUMN IF NOT EXISTS produced_memory_ids jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN IF NOT EXISTS correlation_id text;

UPDATE analysis_jobs SET correlation_id = outbox_job_id::text
WHERE correlation_id IS NULL AND outbox_job_id IS NOT NULL;

ALTER TABLE analysis_jobs ALTER COLUMN outbox_job_id DROP NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS analysis_jobs_correlation_idx ON analysis_jobs (correlation_id);
