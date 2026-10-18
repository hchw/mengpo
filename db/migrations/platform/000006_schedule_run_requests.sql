-- Manual run requests for tenant schedules. A console operator sets
-- run_requested_at to ask the worker's scheduler to fire that schedule on its
-- next tick, without waiting for the cadence. The column is deliberately
-- separate from next_run_at: the in-memory scheduler owns next_run_at while it
-- runs, so only the scheduler may clear this request flag.
ALTER TABLE tenant_schedules ADD COLUMN IF NOT EXISTS run_requested_at timestamptz;

CREATE INDEX IF NOT EXISTS tenant_schedules_run_requested_idx
	ON tenant_schedules (run_requested_at)
	WHERE run_requested_at IS NOT NULL;
