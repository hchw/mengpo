# Mengpo Memory Service — Operations Guide

This guide covers deployment, secrets, migrations, backup/restore, rollback and
capacity for the Mengpo memory service. It is written to be rehearsable: every
procedure below has a matching drill in the test suite or a `make` target.

## 1. Topology

| Component | Binary / image | Responsibility |
| --- | --- | --- |
| API | `cmd/memory-server` (`deploy/Dockerfile` with `SERVICE=memory-server`) | Observe / Project / Consolidate / Feedback / Session HTTP API |
| Worker | `cmd/memory-worker` (`SERVICE=memory-worker`) | Durable outbox jobs: normalize, analyze, embed, consolidate |
| Console | `deploy/Dockerfile.console` | React Memory Console served by nginx, proxying `/api` and `/auth` |
| PostgreSQL + pgvector | `pgvector/pgvector:pg16` | Tenant-per-schema storage, vector index, outbox, audit |
| Redis | `redis:7-alpine` | Tenant-scoped cache (keys embed tenant id) |
| NATS | `nats:2.10-alpine` | Wake-up notifications only (not a durability boundary) |

NATS is **not** the source of truth. PostgreSQL outbox jobs are durable; a
missed NATS notification is harmless because the worker independently polls.

## 2. Single-machine development

```bash
make dev          # docker compose up --build (Postgres, NATS, API, worker, console)
make dev-down     # tear down including volumes
```

The compose file is `deploy/dev/docker-compose.yml`. Console: http://localhost:5173.
API: http://localhost:8080. NATS monitoring: http://localhost:8222.

## 3. Production deployment

1. Build the API and worker images from `deploy/Dockerfile` with
   `--build-arg SERVICE=memory-server` / `memory-worker`, and the console from
   `deploy/Dockerfile.console`.
2. Provide PostgreSQL with the `vector` extension available (pgvector image or
   an extension-enabled cluster). Platform migrations run on startup and create
   `public.platform_migrations`, the tenant registry and quarantine sink.
3. Run at least one worker per tenant shard. Workers are idempotent and lease
   outbox jobs, so N replicas are safe.
4. Terminate TLS at the ingress; the console proxies `/api` and `/auth` to the
   API service.

### Required configuration

| Variable | Purpose |
| --- | --- |
| `MEMORY_DATABASE_URL` | PostgreSQL DSN (per environment) |
| `MEMORY_NATS_URL` | NATS URL; optional (worker falls back to polling) |
| `MEMORY_REDIS_URL` | Redis URL for the tenant-scoped cache |
| `MEMORY_HTTP_ADDR` | API listen address |
| `MEMORY_QUEUE_ADAPTER` | `nats-core` (default) or `none` for PostgreSQL polling only |
| `MEMORY_EMBEDDING_ARTIFACT` | Path to the local embedding GGUF artifact |
| `MEMORY_MIGRATE_ON_START` | Run platform migrations on startup (default `true`) |

All keys use the `MEMORY_` prefix; unprefixed legacy names (for example
`DATABASE_URL`, `HTTP_ADDR`, `MQ_URL`) are accepted during the migration window
only. See `.env.example` for the full canonical list.

### Secrets

- Never bake credentials into images; inject DSNs and provider keys via the
  orchestrator's secret store.
- Agent credentials are stored as SHA-256 verifiers only; the raw secret is
  returned once at registration/rotation. See `internal/application/agents`.
- Rotate agent credentials with `RotateCredential`, which atomically replaces
  the verifier and revokes the previous one. Disable an agent with
  `DisableAgent`; handshakes then fail.
- Security events (`provider`, `api`, `data_access`, `export`, `delete`,
  `audit`) are emitted as structured logs with mandatory tenant and actor
  identity. Sensitive-looking detail fields are redacted automatically.

## 4. Migrations

Migrations live in `db/migrations/platform` and `db/migrations/tenant` and are
embedded in the binary. They are versioned and applied within an advisory lock.

- **Forward**: applied automatically on startup and during tenant provisioning.
  Re-running is a no-op (already-applied versions are skipped).
- **Rollback**: `RollbackProvisioningTenant` reverts exactly one latest tenant
  migration and is **only** allowed for a not-yet-enabled (provisioning) tenant
  schema. Enabling a tenant after a full rollback is rejected.

Drill: `go test ./internal/platform/registry -run 'TestMigrationRollback'` with
`MEMORY_TEST_DATABASE_URL` set.

## 5. Backup and restore

Backup (per environment):

```bash
pg_dump --format=custom --no-owner "$MEMORY_DATABASE_URL" > mengpo.dump
```

Restore into a fresh cluster:

```bash
pg_restore --clean --if-exists --no-owner --dbname "$RESTORE_DATABASE_URL" mengpo.dump
```

Restore correctness relies on tenant schemas being self-contained: each tenant
owns its schema and its `tenant_migrations` ledger, and the platform database
owns the tenant registry. After restore, the API re-applies any migrations that
the dump predates before serving traffic.

Drill: `go test ./internal/platform/registry -run TestRestoreReappliesMigrations`.

## 6. Rollback

- **Application**: deploy the previous image; the API and worker are stateless
  apart from PostgreSQL, so a rollback is an image swap.
- **Tenant schema**: only provisioning tenants can roll back a migration. Once a
  tenant is enabled, forward-only migrations apply; ship a corrective migration
  instead of a down migration.
- **Console**: the static bundle is content-addressed; redeploy the previous
  build artifacts.

## 7. Capacity and SLO

Baseline SLOs (see `deploy/benchmarks.md` for measured numbers):

| Path | p95 latency | Notes |
| --- | --- | --- |
| Structured / full-text recall | < 150 ms | No embedding required |
| Hybrid recall (vector on) | < 400 ms | Depends on pgvector index |
| Memory LLM consolidation | < 20 s | Asynchronous, retried via outbox |
| Degraded recall | < 150 ms | Falls back to structured/full-text + session context |

Capacity planning:

- Scale workers horizontally; outbox leasing prevents duplicate processing.
- Watch queue depth, oldest job age, dead-letter count and LLM error rate
  (`internal/observability.EvaluateAlerts`).
- Embedding jobs are batchable per tenant; keep batch size bounded so a single
  tenant cannot monopolise a worker.
- The retrieval cache is tenant-scoped and cleared on tenant switch; size it by
  distinct query hashes per tenant, not by user count.