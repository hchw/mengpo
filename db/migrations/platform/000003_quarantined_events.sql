-- Quarantine sink for observation envelopes that arrive without a trusted
-- tenant credential. Quarantined events are stored outside every tenant schema
-- and are never normalized into memory until an operator resolves the tenant.
CREATE TABLE IF NOT EXISTS quarantined_events (
    id uuid PRIMARY KEY,
    received_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL,
    declared_tenant text,
    principal_type text,
    principal_id text,
    request_id text,
    credential_fingerprint text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS quarantined_events_received_idx ON quarantined_events (received_at DESC);
