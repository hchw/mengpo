CREATE TABLE IF NOT EXISTS public.users (
    id uuid PRIMARY KEY,
    email text NOT NULL UNIQUE,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('provisioning', 'active', 'suspended', 'deleting')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.tenant_schema_registry (
    tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id) ON DELETE CASCADE,
    schema_name text NOT NULL UNIQUE,
    migration_version integer NOT NULL DEFAULT 0 CHECK (migration_version >= 0),
    state text NOT NULL CHECK (state IN ('provisioning', 'enabled', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_schema_registry_name_format CHECK (schema_name ~ '^tenant_[a-f0-9]{32}$')
);

CREATE TABLE IF NOT EXISTS public.permissions (
    code text PRIMARY KEY,
    description text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS public.roles (
    id uuid PRIMARY KEY,
    tenant_id uuid REFERENCES public.tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS roles_tenant_name_idx
    ON public.roles (tenant_id, name)
    WHERE tenant_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS roles_platform_name_idx
    ON public.roles (name)
    WHERE tenant_id IS NULL;

CREATE TABLE IF NOT EXISTS public.role_permissions (
    role_id uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
    permission_code text NOT NULL REFERENCES public.permissions(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_code)
);

CREATE TABLE IF NOT EXISTS public.tenant_memberships (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    status text NOT NULL CHECK (status IN ('invited', 'active', 'suspended', 'removed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, tenant_id)
);

CREATE INDEX IF NOT EXISTS tenant_memberships_user_status_idx
    ON public.tenant_memberships (user_id, status);

CREATE TABLE IF NOT EXISTS public.membership_roles (
    membership_id uuid NOT NULL REFERENCES public.tenant_memberships(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES public.roles(id) ON DELETE CASCADE,
    PRIMARY KEY (membership_id, role_id)
);

CREATE TABLE IF NOT EXISTS public.agents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    name text NOT NULL,
    allowed_user_ids uuid[] NOT NULL DEFAULT '{}',
    allowed_session_ids uuid[] NOT NULL DEFAULT '{}',
    allowed_scopes text[] NOT NULL,
    capabilities text[] NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agents_allowed_scopes_check CHECK (allowed_scopes <@ ARRAY['user-global', 'session']::text[]),
    UNIQUE (id, tenant_id)
);

CREATE INDEX IF NOT EXISTS agents_tenant_status_idx
    ON public.agents (tenant_id, status);

CREATE TABLE IF NOT EXISTS public.agent_credentials (
    id text PRIMARY KEY,
    agent_id uuid NOT NULL REFERENCES public.agents(id) ON DELETE CASCADE,
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    secret_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_credential_agent_tenant_fk FOREIGN KEY (agent_id, tenant_id)
        REFERENCES public.agents(id, tenant_id) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE IF NOT EXISTS public.auth_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    tenant_id uuid REFERENCES public.tenants(id) ON DELETE CASCADE,
    membership_id uuid REFERENCES public.tenant_memberships(id) ON DELETE SET NULL,
    token_hash bytea NOT NULL UNIQUE,
    authz_version bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    revocation_note text NOT NULL DEFAULT '',
    CHECK (expires_at > created_at)
);

CREATE INDEX IF NOT EXISTS auth_sessions_user_active_idx
    ON public.auth_sessions (user_id, expires_at)
    WHERE revoked_at IS NULL;
