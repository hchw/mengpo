DROP TABLE IF EXISTS provider_configs;
DROP TABLE IF EXISTS maintenance_cursors;
DROP INDEX IF EXISTS analysis_jobs_session_created_idx;
DROP INDEX IF EXISTS analysis_jobs_task_status_created_idx;
ALTER TABLE analysis_jobs
    DROP COLUMN IF EXISTS trigger_source,
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS tokens_prompt,
    DROP COLUMN IF EXISTS tokens_completion,
    DROP COLUMN IF EXISTS latency_ms,
    DROP COLUMN IF EXISTS cost_usd,
    DROP COLUMN IF EXISTS candidate_count,
    DROP COLUMN IF EXISTS discarded_count,
    DROP COLUMN IF EXISTS conflict_count,
    DROP COLUMN IF EXISTS degraded_reason,
    DROP COLUMN IF EXISTS input_event_ids;
