# Acceptance Matrix

Each row maps an acceptance area to the implementation and the automated test
that proves it. Tests that need PostgreSQL run under
`MEMORY_TEST_DATABASE_URL` (see `make test-db`); the rest run with `make test`
and `make test-web`.

## Tenant identity (cross-tenant contamination)

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Cache keys carry tenant identity | `internal/ports/cache.go`, `internal/application/projection/persistence.go` | `internal/verification/tenant_identity_test.go`, `internal/adapters/redis/cache_test.go` |
| Redis cache isolates tenants | `internal/adapters/redis/cache.go` | `cache_test.go` (`TestRedisCacheIsolatesTenantsForSameLogicalKey`) |
| Task queue / NATS notifications carry tenant | `internal/adapters/nats/pubsub.go` | `pubsub_test.go` (`TestNotificationsKeepTenantsSeparateAndDropUnboundPayloads`) |
| Embedding jobs carry tenant | `internal/application/embedding/worker.go` | `internal/verification` (`TestEmbeddingWorkerRequiresTenantIdentity`) |
| Memory LLM batches carry tenant | `internal/application/analysis/service.go` | `internal/verification` (`TestAnalysisBatchCarriesTenantIdentity`) |
| Metrics labeled per tenant | `internal/observability/tenant.go`, `http.go` | `internal/observability/tenant_metrics_test.go` |

## API

| Requirement | Implementation | Verification |
| --- | --- | --- |
| v1 Envelope contract, strict decode/validation | `internal/api/dto/envelope.go` | `internal/api/dto/envelope_test.go` |
| Observe / Project / Consolidate / Feedback / Session routes | `internal/api/httpapi/server.go` | `internal/api/httpapi/server_test.go` |
| Tenant-bound Envelope + cross-tenant rejection | `internal/application/agentaccess/access.go` | `internal/application/agentaccess/access_test.go` |
| Level 0/1/2 tenant resolution | `internal/application/agentaccess/resolve.go` | `resolve_test.go`, `TestLevel0DeploymentCredentialResolvesTenantAndIngests` |
| Agent handshake, capability negotiation, scope | `internal/application/agents/handshake.go` | `handshake_test.go` |

## Database

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Tenant-per-schema routing and lifecycle | `internal/platform/registry/store.go` | `internal/platform/registry/lifecycle_test.go` |
| Forward-only tenant migrations + provisioning rollback | `db/migrations` | `TestMigrationRollback*`, `TestRestoreReappliesMigrations` |
| Tenant-scoped repository isolation | `internal/adapters/postgres/*.go` | `session_test.go`, `TestBindSessionIsIdempotentAndTenantScoped` |
| Quarantine outside tenant schemas | `internal/adapters/postgres/quarantine.go` | `quarantine_test.go` |

## Events

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Atomic raw-event + outbox write, lease recovery | `internal/adapters/postgres/observation.go`, `outbox.go` | `outbox_integration_test.go` |
| NATS wake-up is not a durability boundary | `internal/adapters/nats/pubsub.go` | `pubsub_test.go` |
| Attribution levels | `internal/domain/observation/attribution.go` | `attribution_test.go` |
| Full evidence chain Observe→Normalize→Project→Failure→Feedback | `internal/application/analysis/pipeline.go` | `internal/adapters/postgres/end_to_end_test.go` |

## LLM

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Provider abstraction + output validation | `internal/application/analysis/*` | `service_test.go`, `validate_test.go` |
| Privacy filtering / redaction before provider calls | `internal/application/analysis/privacy.go` | `privacy_test.go` |
| Rule-based degradation never creates memory | `internal/application/analysis/fallback.go` | `fallback_test.go` |
| LLM error-rate alerting | `internal/observability/alerts.go` | `alerts_test.go` |

## Retrieval

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Five-channel hybrid recall | `internal/application/recall/service.go` | `service` tests, `labeled_set_test.go` |
| Explainable ranking + budgets + dedup | `internal/application/recall/ranking.go`, `projection/budget.go` | `ranking_test.go`, `budget_test.go` |
| Independent reranker + transfer guard | `internal/application/recall/rerank.go`, `transfer.go` | `rerank_test.go`, `transfer_test.go` |
| Degradation to structured/full-text | `internal/application/recall/rerank.go` | `fallback_test.go` |
| Focus/Divergence orchestration | `internal/application/projection/orchestrator.go` | `orchestrator_test.go` |
| Timeout/cache/session-context degradation | `internal/application/projection/pipeline.go` | `pipeline_test.go` |

## Frontend

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Tenant-scoped API client, pagination, cache, errors | `web/src/api/client.ts` | `client.test.ts` |
| Login/SSO, tenant list/switch, cache clear | `web/src/auth/*` | `auth.test.tsx` |
| Dashboard / Session Explorer / Background Memory | `web/src/pages/*` | `pages.test.tsx`, `AppRoutes.test.tsx` |
| Candidate review with optimistic update + conflict | `web/src/pages/CandidateReviewPage.tsx` | `pages.test.tsx` |
| Projection Debugger | `web/src/pages/ProjectionDebuggerPage.tsx` | `pages.test.tsx` |
| Failure Analysis / Evaluation / Members / Settings | `web/src/pages/*` | `pages.test.tsx` |
| loading/empty/error/permission/conflict/stale/tenant-switch/rollback states | `web/src/components/states.tsx` | `states.test.tsx` |

## Operations

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Single-machine dev environment | `deploy/dev/docker-compose.yml`, `Makefile` | `docker compose config` |
| Deployment / secrets / migration / backup / restore / rollback / capacity | `docs/operations.md` | `TestRestoreReappliesMigrations`, rollback tests |
| Performance benchmarks + SLO | `internal/application/recall/benchmark_test.go` | `make bench`, `deploy/benchmarks.md` |
| Security logging (provider/api/data/export/delete/audit) | `internal/observability/audit.go` | `audit_test.go` |
| Tracing, queue backlog, dead-letter, LLM error, degradation alerts | `internal/observability/alerts.go` | `alerts_test.go` |
| Offline datasets + evaluation metrics | `internal/application/evaluation/*` | `dataset_test.go`, `metrics_test.go`, `task_test.go` |

## Known gaps

- The provider *models* (embedding/reranker/LLM) are configured artifacts; the
  acceptance above covers the adapters and degradation paths, not model quality,
  which is tracked by the offline evaluation datasets.

## Runtime assembly

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Config loaded and validated at startup | `internal/config`, `internal/assembly/boot.go` | `internal/config/env_naming_test.go`, `boot_test.go` |
| Canonical `MEMORY_` env names across config/compose/docs | `internal/config/config.go`, `.env.example`, `deploy/dev/docker-compose.yml`, `docs/operations.md` | `internal/config/env_naming_test.go` |
| Server wired to PostgreSQL, Tenant Router, repositories and `agentaccess.Service` | `internal/assembly/server.go` | `internal/assembly/server_integration_test.go` (`TestHTTPCommandEndpoints`) |
| Consolidation enqueued as a durable job | `internal/adapters/postgres/outbox.go` (`EnqueueJob`) | `TestHTTPCommandEndpoints` |
| Worker wired to outbox runner, Pub/Sub and embedding loop with graceful shutdown | `internal/assembly/worker.go` | `internal/assembly/worker_test.go` |
| Platform migrations run on startup, idempotently | `internal/assembly/db.go`, `internal/platform/registry` | `TestMigrateIsIdempotent` |

## Scope decisions

- The Memory LLM real provider adapter is **out of scope**. The provider
  interface, redaction, output validation and rule-based degradation
  (`analysis.RuleFallback`) are the supported baseline; there is no external LLM
  inference integration in this change.
- Chinese code cross-language retrieval stays a Chinese-paraphrase smoke check;
  no additional cross-language quality benchmark is introduced.
- Candidate **merge** is a distinct primitive from `supersede`: it unions the
  duplicates' evidence into the canonical target, raises confidence, retires the
  duplicates, and links them with a `merged_into` relation (evidence is never
  deleted). It is implemented in `internal/adapters/postgres/merge.go` and
  covered by `internal/assembly/console_integration_test.go`.
## Memory LLM analyst and per-tenant providers

| Requirement | Implementation | Verification |
| --- | --- | --- |
| Configurable Memory LLM provider (per tenant) | `internal/application/providerconfig`, `internal/adapters/postgres/provider_config.go` | `internal/application/providerconfig/service_test.go` (`TestEffectivePrecedence`), `internal/adapters/postgres/provider_config_test.go` |
| Structured, validated candidate output | `internal/adapters/llm/http.go`, `internal/application/analysis/validate.go` | `internal/adapters/llm/http_test.go` (`TestAnalyzeRejectsEvidenceNotInBatch`) |
| Never bypasses evidence or governance | `internal/adapters/postgres/candidate.go`, `internal/assembly/jobs.go` | `internal/assembly/analyst_e2e_test.go`, `internal/adapters/postgres/candidate_test.go` |
| Periodic memory maintenance (per tenant) | `internal/application/maintenance`, `internal/assembly/worker.go` | `internal/application/maintenance/maintenance_test.go` |
| Trigger gating and batching | `internal/domain/observation/signals.go`, `internal/assembly/jobs.go` | `internal/domain/observation/signals_analysis_test.go`, `TestOrdinaryEventNeverCallsModelOrCreatesCandidates`, `TestIdenticalConsolidationReusesCachedResult` |
| Failure handling and degradation | `internal/application/analysis/fallback.go` | `TestDegradedAnalysisRunIsRecorded`, `internal/application/analysis/fallback_test.go` |
| Tenant isolation and privacy | `internal/application/analysis/privacy.go`, `internal/assembly/provider.go` | `TestPrivacyRedactionAppliedBeforeRequest`, `TestProviderBuilderHonoursExternalFlag` |
| Observable and audited runs | `internal/ports/analyst_ops.go`, `internal/adapters/postgres/analyst_runs.go` | `internal/adapters/postgres/analyst_runs_test.go`, `internal/observability/ops_metrics_test.go` |
| Replaceable scheduler component | `internal/ports/scheduler.go`, `internal/adapters/scheduler` | `internal/adapters/scheduler/scheduler_test.go` |
| Per-tenant schedules | `db/migrations/platform/000004_tenant_schedules.sql`, `internal/adapters/postgres/schedule.go` | `internal/adapters/postgres/schedule_test.go` |
| Single execution across replicas | `internal/adapters/postgres/advisory.go` | `internal/adapters/postgres/advisory_test.go` |
| Tenant-scoped configuration API and visibility | `internal/assembly/console_ops.go`, `web/src/pages/AnalysisRunsPage.tsx`, `web/src/pages/SettingsPage.tsx` | `internal/assembly/console_ops_test.go`, `web/src/pages/pages.test.tsx` |
