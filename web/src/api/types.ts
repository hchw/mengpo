// Shared API types. These mirror the Go v1 Envelope contract in
// internal/api/dto and are kept intentionally small: the console renders what
// the tenant-scoped API returns and never invents authorization state.

export type ScopeType = 'user-global' | 'session';
export type MemoryMode = 'auto' | 'focus' | 'diverge';
export type MemoryStatus = 'candidate' | 'active' | 'stable' | 'conflicted' | 'rejected' | 'expired';

export interface Tenant {
  id: string;
  name: string;
  status: 'provisioning' | 'active' | 'suspended' | 'deleting';
  role: 'owner' | 'tenant-admin' | 'member';
}

export interface Principal {
  type: 'user' | 'agent';
  id: string;
}

export interface EnvelopeScope {
  tenant_id: string;
  user_id: string;
  type: ScopeType;
  session_id?: string;
}

export interface Envelope<T = unknown> {
  version: 'v1';
  request_id: string;
  idempotency_key: string;
  principal: Principal;
  scope: EnvelopeScope;
  memory_hint?: { mode?: MemoryMode; topics?: string[]; memory_ids?: string[]; allow_candidates?: boolean };
  budget?: { candidates?: number; ranking?: number; injection_tokens?: number; relation_depth?: number };
  privacy: { visibility: 'private' | 'tenant'; allow_external_llm?: boolean; sensitive_fields?: string[]; include_raw_evidence?: boolean };
  payload: T;
}

export interface ApiErrorBody {
  code: string;
  message: string;
  retryable: boolean;
}

export interface ApiEnvelopeResponse<T> {
  version: string;
  request_id?: string;
  data?: T;
  error?: ApiErrorBody;
}

export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}

export interface SessionSummary {
  id: string;
  title: string;
  status: 'active' | 'closing' | 'closed' | 'interrupted';
  started_at: string;
  updated_at: string;
}

export interface MemoryRecord {
  id: string;
  type: string;
  status: MemoryStatus;
  scope_type: ScopeType;
  content_summary: string;
  confidence: number;
  updated_at: string;
}

// MemoryDetailEvent is one raw event that supports a memory, previewed and
// truncated server-side.
export interface MemoryDetailEvent {
  id: string;
  source_type: string;
  message_type: string;
  session_id: string;
  occurred_at: string;
  payload: string;
}

// MemoryDetail is a memory plus the content behind it: the structured payload,
// the rendered text, and the raw events that support it. run_id, when present,
// points at the curated task that produced the memory.
export interface MemoryDetail extends MemoryRecord {
  content_text: string;
  content: unknown;
  created_at: string;
  evidence: MemoryDetailEvent[];
  run_id?: string;
  provenance?: unknown;
}

export interface CandidateRecord extends MemoryRecord {
  proposed_by: string;
  evidence_ids: string[];
  provider?: string;
  model?: string;
  prompt_version?: string;
  triage_group?: string;
  triage_conflicts?: string[];
  triage_priority?: number;
  triage_rationale?: string;
}

// DuplicateGroupMember is one memory in an advisory duplicate group, returned by
// the candidate review surface when a reviewer opens a suspected duplicate.
export interface DuplicateGroupMember {
  id: string;
  status: string;
  scope_type: string;
  confidence: number;
  content: string;
  created_at: string;
}

export interface RankingReason {
  relevance: number;
  confidence: number;
  coverage: number;
  freshness: number;
  applicability_hit: boolean;
  reranker_score?: number | null;
  channels: string[];
}

export interface ProjectionCandidate {
  memory_id: string;
  included: boolean;
  score: number;
  reason?: RankingReason;
  excluded_reason?: string;
  provenance: string[];
}

export interface ProjectionDebug {
  mode: MemoryMode;
  reason: string;
  cache_hit: boolean;
  degraded: boolean;
  degraded_reasons: string[];
  budget: { candidates: number; ranking: number; injection_tokens: number };
  usage: { candidates_seen: number; candidates_ranked: number; candidates_injected: number; tokens_injected: number };
  candidates: ProjectionCandidate[];
}

export interface FailureRecord {
  id: string;
  title: string;
  confidence: 'confirmed' | 'inferred' | 'suspected' | 'unknown';
  attribution: 'direct' | 'correlated' | 'inferred' | 'unknown';
  related_memory_ids: string[];
}

export interface EvaluationSnapshot {
  generated_at: string;
  retrieval_precision: number;
  promotion_precision: number;
  wrong_memory_rate: number;
  attribution_accuracy: number;
  latency_ms_p95: number;
  tokens_per_projection: number;
  cost_usd: number;
  cache_hit_rate: number;
  // has_data distinguishes "measured 0%" from "nothing recorded yet";
  // projections/feedback_samples state the sample size behind each rate.
  has_data?: boolean;
  projections?: number;
  feedback_samples?: number;
  // measured_metrics lists the metrics actually derived from telemetry. Keys
  // absent from it must render as unmeasured, never as 0%.
  measured_metrics?: string[];
}

export interface Member {
  user_id: string;
  email: string;
  role: 'owner' | 'tenant-admin' | 'member';
  status: 'invited' | 'active' | 'suspended' | 'removed';
}

export interface AgentRecord {
  id: string;
  name: string;
  status: 'active' | 'disabled';
  capabilities: string[];
  allowed_scopes: ScopeType[];
}

export interface AgentRegistrationInput {
  name: string;
  allowed_user_ids?: string[];
  allowed_session_ids?: string[];
  allowed_scopes: ScopeType[];
  capabilities: string[];
}

// AgentRegistration is the one-time response of agent creation; secret is shown
// exactly once and never retrievable again.
export interface AgentRegistration {
  id: string;
  name: string;
  status: 'active' | 'disabled';
  capabilities: string[];
  allowed_scopes: ScopeType[];
  secret: string;
}

export interface ProviderView { provider: string; enabled: boolean; base_url: string; model: string; has_secret: boolean; secret_hint: string; source: 'tenant' | 'env' | 'disabled'; updated_by?: string; updated_at?: string;
}
export interface ProviderUpdate { enabled: boolean; base_url: string; model: string; api_key?: string;
}
export interface ProviderTestResult { ok: boolean; latency_ms: number; error: string;
}
export interface ScheduleStatus { name: string; cadence_seconds: number; next_run_at: string; last_run_at: string; last_status: string; last_error: string; runs: number;
}
export interface AnalysisRun { id: string; task_type: string; trigger: string; provider: string; model: string; prompt_version: string; status: string; latency_ms: number; tokens_prompt: number; tokens_completion: number; candidate_count: number; discarded_count: number; conflict_count: number; degraded_reason: string; last_error: string; session_id: string; input_count: number; output_count: number; created_at: string;
}
export interface AnalysisRunInput { id: string; source_type: string; message_type: string; session_id: string; occurred_at: string; payload: string;
}
export interface AnalysisRunOutput { id: string; status: string; memory_type: string; scope_type: string; confidence: number; content_text: string; created_at: string;
}
export interface AnalysisRunDetail { run: AnalysisRun; result: unknown; inputs: AnalysisRunInput[]; outputs: AnalysisRunOutput[]; truncated: boolean;
}
export interface ScheduleUpdate { name: string; cadence_seconds: number; enabled: boolean;
}
