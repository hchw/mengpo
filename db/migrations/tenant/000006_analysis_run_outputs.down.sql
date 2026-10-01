DROP INDEX IF EXISTS analysis_jobs_correlation_idx;
ALTER TABLE analysis_jobs ALTER COLUMN outbox_job_id SET NOT NULL;
ALTER TABLE analysis_jobs
    DROP COLUMN IF EXISTS correlation_id,
    DROP COLUMN IF EXISTS produced_memory_ids;
