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
| `MEMORY_SCHEDULER_ADAPTER` | Periodic scheduler: `internal` (default) or `none` |
| `MEMORY_SCHEDULE_DEFAULT_INTERVAL` | Default per-tenant schedule cadence (default `24h`) |
| `MEMORY_CONFIG_MASTER_KEY` | 32-byte master key (base64/hex/raw) encrypting per-tenant provider secrets |

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
- **Provider secrets** are encrypted at rest with AES-256-GCM under
  `MEMORY_CONFIG_MASTER_KEY`. The key is injected from the secret store and is
  never written to the database or image. If the key is missing, saving a
  provider secret is rejected — the service never falls back to plaintext.
  Without the key you can still run the environment-default provider.

### Recreating the API container

The console image runs nginx with `proxy_pass http://memory-server:8080`; nginx
resolves that upstream once at startup. If you recreate or reassign the API
container, also restart the console so it re-resolves the address, otherwise the
console returns `502 Bad Gateway` even though the API is healthy:

```sh
docker compose -f deploy/dev/docker-compose.yml up -d --force-recreate memory-server console
```

### Verifying memory injection

`cmd/verify-injection` is a runnable end-to-end check of the injection path
against a live service. It only uses the public HTTP API and exits non-zero on
the first failed check:

```sh
go run ./cmd/verify-injection                       # 12 memories, 8 injections
go run ./cmd/verify-injection -rounds 30            # stress: 30 sequential injections
go run ./cmd/verify-injection -corpus 12 -verbose
go run ./cmd/verify-injection -base http://localhost:5173
go run ./cmd/verify-injection -seed=false           # never touch the database
```

It seeds a **labelled corpus** of long, realistic memories (one distinctive
keyword each) and then injects a series of queries whose expected answer is
known, so effectiveness — not just "something came back" — is measured:

| Check | Expectation |
| --- | --- |
| Precision | every injected memory mentions the query term (no unrelated memory) |
| Recall | the labelled memory for that query is always injected |
| Determinism | repeating a query injects the same memory set |
| Duplicates | no repeated entry within one projection |
| Ranking | injected items are ordered by non-increasing score |
| Metering | sum of item token costs equals `usage.TokensInjected` |
| Budget | `usage.TokensInjected` never exceeds `injection_tokens` |
| Truncation | a small budget truncates long content; a large budget keeps it whole |
| Feedback | `POST /api/v1/feedback` stores a signal for an injected memory |

Step 6 covers the **whole-session observation** path: it opens a working session
and a control session, pushes a realistic transcript (a tool failure, a user
correction, two "remember" instructions) plus events with no memory intent, and
then checks that the session was ingested and that the rule pre-screen triggered
analysis runs. Verifying that the analysis *produces a retained memory and
injects it inside the session* needs a real LLM, so it is opt-in:

```sh
go run ./cmd/verify-injection -session-memory \
  -provider-base-url https://api.example.com/v1 -provider-model some-model -provider-api-key sk-...
```

With `-session-memory` the tool also confirms the resulting session memory and
checks that it is injected inside that session but **not** visible from another
session.

The run ends with an aggregate report (precision/recall, violation counts,
p50/p95 latency, cache hit rate, retrieval modes). Seeding uses
`MEMORY_DATABASE_URL` and only writes the tenant's fixture rows; it is
idempotent.

Notes:

- Payload validation errors (`dto.ErrInvalidEnvelope`, `observation.ErrInvalidPrincipal`)
  carry API error codes, so bad requests answer `400` instead of `500`.
- `memory_hint.memory_ids` is accepted and echoed but does not force-inject
  those memories; only the hint mode affects retrieval.
- Recall uses the full-text channel on `content_text`; memories without an
  embedding are still injected, but a query only matches terms that literally
  appear in the text (the `simple` text search configuration does not stem and
  does not segment CJK).

### Memory LLM curation and per-tenant providers

- The Memory LLM is the only model analysis provider for curation. It is a
  **candidate analyser**: it only ever produces candidates
  (`status=candidate`, `default_retrieval=false`) that must pass evidence
  validation and governance before promotion.
- **Provider configuration is per tenant.** A tenant administrator configures
  its own provider in the console (Settings → Memory LLM provider): enable,
  base URL, model, API key and timeouts. The API key is write-only: it is never
  returned, only a masked hint is shown.
- **Precedence**: tenant-stored configuration wins over the environment default
  (`MEMORY_LLM_BASE_URL`/`MEMORY_LLM_MODEL`/`MEMORY_LLM_API_KEY`), which wins
  over disabled. A tenant that explicitly disables the provider does not fall
  back to the environment default.
- **Changes apply without a restart.** Saved configuration is hot-swapped;
  worker replicas pick it up via a short-TTL cache, so no rollout is required.
  A configuration that fails validation is rejected and the running one is kept.
- **Trigger gating**: ordinary events are normalized but never sent to the
  model. Only failure/retry/user-correction signals and explicit memory intent
  trigger analysis; periodic maintenance batches the rest. Identical batches
  are de-duplicated via the tenant cache and stable idempotency keys.
- **Every run is recorded.** `analysis_jobs` (surfaced in the console under
  Curation) captures the trigger, model, prompt version, inputs, outcome,
  latency, tokens and the count of candidates/discarded/conflicts, plus the
  degradation reason when the rule fallback was used.
- **Schedules are per tenant.** Each tenant owns its curation plan
  (`tenant_schedules`) with a default daily cadence and can adjust it from the
  console. Multi-replica executions are serialized with a PostgreSQL advisory
  lock, so a trigger runs at most once.

### Deep runtime integration (Level 2 ingress)

The `/api/v1/observe` payload accepts four optional fields that carry a deep
runtime adapter's trace. They are optional, and an adapter that omits them keeps
exactly the previous behaviour.

| Field | Meaning |
| --- | --- |
| `source_type` | `user` (default), `agent`, `tool`, `workflow`, or `gateway`. Selects how the event is categorised; it does **not** affect authorization. |
| `message_type` | The event type. It is now stored as declared; it used to be overwritten with `message`. |
| `sequence` | Ordering within the session, so a reader can rebuild the original order. |
| `parent_event_id` | The parent event, so a causal chain can be rebuilt. |
| `trace` | `task_id`, `attempt_id`, `projection_id`, `used_memory_ids`, `tool_result_id`, `outcome_id`. |

`/api/v1/project` returns `data.projection_id`, the identity of the projection
that produced the response. Send it back inside `trace.projection_id` and the
service derives `used_memory_ids` from the memories that projection actually
exposed — the caller's own claim is never trusted. A cache hit returns the
identity of the projection that produced the cached result, not a new one. A
degraded projection (retrieval timeout or database unavailable) returns local
session context rather than memories, and therefore carries no identifier.

Attribution precision follows the trace: a complete trace records `direct`
attribution, a session-only link records `inferred` with the missing links named,
and no link at all records `unknown`. `direct` describes **link completeness, not
correctness** — it does not change governance, promotion, or confidence.

`/api/v1/project` also accepts an optional `scenario` object so the service, not
the caller, decides how deep to look:

| Field | Meaning |
| --- | --- |
| `clarity` | `clear` (default), `partial`, or `unclear`. |
| `progress_percent` | 0–100. |
| `repeated_failures` | Consecutive failures on the task. |
| `conflict_count` | Conflicting evidence seen. |
| `evidence_gap_count` | Missing evidence seen. |

Repeated failures (≥2), any conflict, any evidence gap, an unclear task below
50% progress, or a stalled partial task select **divergence**; otherwise recall
stays **focused**. Out-of-range values are rejected with `INVALID_ENVELOPE`. A
`memory_hint.mode` remains a bounded preference: a `focus` hint cannot cancel a
safety-triggered divergence, and `memory_hint.allow_candidates: false` can
withhold weak candidates but `true` cannot override policy.

**Deployment order**: request decoding is strict (`DisallowUnknownFields`), so an
older server rejects the new fields with `INVALID_ENVELOPE` (400). Deploy the
server first; clients may then start sending them. Reverting the server is safe —
the fields are simply ignored.

**Behaviour change to expect**: clients that already sent `message_type` while it
was ignored will now see their declared event type participate in rule-based
detection. Events whose meaning was carried only by the event type (rather than
by a payload marker) can therefore start producing analysis runs. Review
analysis-run volume after upgrading.

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