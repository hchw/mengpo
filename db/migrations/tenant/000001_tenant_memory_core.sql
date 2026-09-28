CREATE TABLE IF NOT EXISTS sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    created_by_agent_id uuid,
    status text NOT NULL CHECK (status IN ('active', 'closing', 'closed', 'interrupted')),
    title text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE INDEX IF NOT EXISTS sessions_user_status_updated_idx
    ON sessions (user_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS observed_events (
    id uuid PRIMARY KEY,
    source_event_id text,
    idempotency_key text NOT NULL UNIQUE,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    conversation_id text,
    source_type text NOT NULL CHECK (source_type IN ('user', 'agent', 'tool', 'workflow', 'gateway')),
    source_id text NOT NULL,
    message_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    payload_text text,
    sequence bigint,
    parent_event_id uuid,
    occurred_at timestamptz NOT NULL,
    ingested_at timestamptz NOT NULL DEFAULT now(),
    visibility text NOT NULL DEFAULT 'tenant' CHECK (visibility IN ('private', 'session', 'tenant')),
    reliability text NOT NULL DEFAULT 'unknown' CHECK (reliability IN ('high', 'medium', 'low', 'unknown')),
    retention_class text NOT NULL,
    normalization_status text NOT NULL DEFAULT 'received' CHECK (normalization_status IN ('received', 'normalized', 'quarantined', 'failed')),
    analysis_status text NOT NULL DEFAULT 'pending' CHECK (analysis_status IN ('pending', 'analyzed', 'skipped', 'failed')),
    redaction_status text NOT NULL DEFAULT 'pending' CHECK (redaction_status IN ('pending', 'redacted', 'rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_type, source_id, source_event_id)
);

CREATE INDEX IF NOT EXISTS observed_events_session_sequence_idx
    ON observed_events (session_id, sequence, occurred_at);
CREATE INDEX IF NOT EXISTS observed_events_processing_idx
    ON observed_events (normalization_status, analysis_status, ingested_at);

CREATE TABLE IF NOT EXISTS normalized_events (
    id uuid PRIMARY KEY,
    raw_event_id uuid NOT NULL UNIQUE REFERENCES observed_events(id) ON DELETE CASCADE,
    schema_version text NOT NULL,
    normalized_payload jsonb NOT NULL,
    normalization_version text NOT NULL,
    sequence bigint,
    parent_event_id uuid,
    processing_status text NOT NULL DEFAULT 'normalized' CHECK (processing_status IN ('normalized', 'processed', 'ignored', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS outbox_jobs (
    id uuid PRIMARY KEY,
    job_type text NOT NULL,
    tenant_id uuid NOT NULL,
    aggregate_id uuid,
    idempotency_key text NOT NULL UNIQUE,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'leased', 'running', 'retrying', 'succeeded', 'dead_letter', 'cancelled')),
    priority smallint NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 8 CHECK (max_attempts > 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_owner text,
    lease_until timestamptz,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((status IN ('leased', 'running')) = (lease_until IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS outbox_jobs_claim_idx
    ON outbox_jobs (status, priority DESC, available_at, created_at);
CREATE INDEX IF NOT EXISTS outbox_jobs_tenant_created_idx
    ON outbox_jobs (tenant_id, created_at DESC);

CREATE TABLE IF NOT EXISTS extraction_runs (
    id uuid PRIMARY KEY,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    run_type text NOT NULL,
    input_watermark bigint,
    rule_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    idempotency_key text NOT NULL UNIQUE,
    started_at timestamptz,
    completed_at timestamptz,
    error_code text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS analysis_jobs (
    id uuid PRIMARY KEY,
    outbox_job_id uuid NOT NULL UNIQUE REFERENCES outbox_jobs(id) ON DELETE CASCADE,
    run_id uuid REFERENCES extraction_runs(id) ON DELETE SET NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    task_type text NOT NULL,
    provider text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    prompt_version text NOT NULL DEFAULT '',
    schema_version text NOT NULL DEFAULT '',
    input_watermark bigint,
    result jsonb,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'retrying', 'dead_letter', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS memory_nodes (
    id uuid PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    user_id uuid NOT NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE CASCADE,
    scope_type text NOT NULL CHECK (scope_type IN ('user-global', 'session')),
    scope_id uuid NOT NULL,
    parent_id uuid REFERENCES memory_nodes(id) ON DELETE SET NULL,
    memory_type text NOT NULL,
    status text NOT NULL CHECK (status IN ('candidate', 'active', 'stable', 'conflicted', 'expired', 'rejected')),
    visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'session', 'tenant')),
    confidence double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    applicability jsonb NOT NULL DEFAULT '{}'::jsonb,
    content jsonb NOT NULL,
    content_text text NOT NULL DEFAULT '',
    default_retrieval boolean NOT NULL DEFAULT false,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb,
    embedding_model text,
    embedding_artifact text,
    embedding_version text,
    embedding_status text NOT NULL DEFAULT 'pending' CHECK (embedding_status IN ('pending', 'ready', 'failed', 'stale')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz,
    deleted_at timestamptz,
    CONSTRAINT memory_nodes_scope_session_check CHECK (
        (scope_type = 'session' AND session_id IS NOT NULL AND scope_id = session_id)
        OR (scope_type = 'user-global' AND session_id IS NULL AND scope_id = user_id)
    )
);

CREATE INDEX IF NOT EXISTS memory_nodes_scope_status_updated_idx
    ON memory_nodes (scope_type, scope_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS memory_nodes_session_parent_idx
    ON memory_nodes (session_id, parent_id);
CREATE INDEX IF NOT EXISTS memory_nodes_type_confidence_idx
    ON memory_nodes (memory_type, confidence DESC);

CREATE TABLE IF NOT EXISTS memory_evidence (
    id uuid PRIMARY KEY,
    memory_id uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
    raw_event_id uuid NOT NULL REFERENCES observed_events(id) ON DELETE RESTRICT,
    normalized_event_id uuid REFERENCES normalized_events(id) ON DELETE SET NULL,
    evidence_role text NOT NULL,
    confidence double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
    attribution text NOT NULL DEFAULT 'unknown' CHECK (attribution IN ('direct', 'correlated', 'inferred', 'unknown')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (memory_id, raw_event_id, evidence_role)
);

CREATE INDEX IF NOT EXISTS memory_evidence_raw_event_idx ON memory_evidence (raw_event_id);

CREATE TABLE IF NOT EXISTS memory_relations (
    id uuid PRIMARY KEY,
    source_memory_id uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
    target_memory_id uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
    relation_type text NOT NULL,
    confidence double precision NOT NULL DEFAULT 1 CHECK (confidence >= 0 AND confidence <= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (source_memory_id <> target_memory_id),
    UNIQUE (source_memory_id, target_memory_id, relation_type)
);

CREATE TABLE IF NOT EXISTS memory_feedback (
    id uuid PRIMARY KEY,
    memory_id uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
    user_id uuid NOT NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    feedback_type text NOT NULL CHECK (feedback_type IN ('accepted', 'helpful', 'harmful', 'corrected', 'ignored', 'conflict', 'expired')),
    reason text,
    request_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS memory_feedback_memory_created_idx
    ON memory_feedback (memory_id, created_at DESC);

CREATE TABLE IF NOT EXISTS retrieval_events (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    request_id text NOT NULL,
    query_hash text NOT NULL,
    mode text NOT NULL,
    candidate_count integer NOT NULL DEFAULT 0 CHECK (candidate_count >= 0),
    selected_memory_ids uuid[] NOT NULL DEFAULT '{}',
    excluded_reasons jsonb NOT NULL DEFAULT '{}'::jsonb,
    degraded_mode text,
    latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS retrieval_events_user_created_idx
    ON retrieval_events (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS retrieval_cache (
    cache_key text PRIMARY KEY,
    user_id uuid NOT NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE CASCADE,
    scope_type text NOT NULL CHECK (scope_type IN ('user-global', 'session')),
    response jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS retrieval_cache_expiration_idx ON retrieval_cache (expires_at);

CREATE TABLE IF NOT EXISTS projection_events (
    id uuid PRIMARY KEY,
    request_id text NOT NULL,
    user_id uuid NOT NULL,
    session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
    mode text NOT NULL,
    selected_memory_ids uuid[] NOT NULL DEFAULT '{}',
    selection_reasons jsonb NOT NULL DEFAULT '{}'::jsonb,
    excluded_reasons jsonb NOT NULL DEFAULT '{}'::jsonb,
    budget jsonb NOT NULL DEFAULT '{}'::jsonb,
    provenance jsonb NOT NULL DEFAULT '{}'::jsonb,
    degraded_mode text,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS projection_events_session_created_idx
    ON projection_events (session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS embedding_jobs (
    id uuid PRIMARY KEY,
    memory_id uuid NOT NULL REFERENCES memory_nodes(id) ON DELETE CASCADE,
    model_id text NOT NULL,
    artifact text NOT NULL,
    model_version text NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'retrying', 'failed', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    idempotency_key text NOT NULL UNIQUE,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS embedding_jobs_claim_idx ON embedding_jobs (status, created_at);

CREATE TABLE IF NOT EXISTS audit_events (
    id uuid PRIMARY KEY,
    actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system', 'admin')),
    actor_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    request_id text NOT NULL,
    changes jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_events_resource_created_idx
    ON audit_events (resource_type, resource_id, created_at DESC);
