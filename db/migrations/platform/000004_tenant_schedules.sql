-- Per-tenant schedule definitions. Each tenant owns its own cadence and enabled
-- state so one tenant's change never affects another. Kept in the platform
-- schema because the scheduler iterates tenants across schemas.
CREATE TABLE IF NOT EXISTS tenant_schedules (
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    cadence_seconds integer NOT NULL CHECK (cadence_seconds > 0),
    enabled boolean NOT NULL DEFAULT true,
    last_run_at timestamptz,
    next_run_at timestamptz,
    last_status text NOT NULL DEFAULT 'pending',
    last_error text NOT NULL DEFAULT '',
    runs bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS tenant_schedules_tenant_idx ON tenant_schedules (tenant_id, name);
