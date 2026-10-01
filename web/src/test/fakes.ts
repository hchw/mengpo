import { vi } from 'vitest';
import type { ConsoleApi } from '../api/console';
import type {
  AgentRecord,
  CandidateRecord,
  EvaluationSnapshot,
  FailureRecord,
  Member,
  MemoryRecord,
  Page,
  ProjectionDebug,
  SessionSummary,
} from '../api/types';

export function page<T>(items: T[], overrides: Partial<Page<T>> = {}): Page<T> {
  return { items, total: items.length, page: 1, page_size: 20, ...overrides };
}

export const session = (id: string, overrides: Partial<SessionSummary> = {}): SessionSummary => ({
  id,
  title: `Session ${id}`,
  status: 'active',
  started_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-02T00:00:00Z',
  ...overrides,
});

export const memory = (id: string, overrides: Partial<MemoryRecord> = {}): MemoryRecord => ({
  id,
  type: 'preference',
  status: 'active',
  scope_type: 'user-global',
  content_summary: `Memory ${id}`,
  confidence: 0.8,
  updated_at: '2024-01-02T00:00:00Z',
  ...overrides,
});

export const candidate = (id: string, overrides: Partial<CandidateRecord> = {}): CandidateRecord => ({
  ...memory(id),
  status: 'candidate',
  proposed_by: 'analyst',
  evidence_ids: [`evidence-${id}`],
  ...overrides,
});

export const failure = (id: string, overrides: Partial<FailureRecord> = {}): FailureRecord => ({
  id,
  title: `Failure ${id}`,
  confidence: 'confirmed',
  attribution: 'direct',
  related_memory_ids: [],
  ...overrides,
});

export const member = (id: string, overrides: Partial<Member> = {}): Member => ({
  user_id: id,
  email: `${id}@example.com`,
  role: 'member',
  status: 'active',
  ...overrides,
});

export const agent = (id: string, overrides: Partial<AgentRecord> = {}): AgentRecord => ({
  id,
  name: `Agent ${id}`,
  status: 'active',
  capabilities: ['observe'],
  allowed_scopes: ['session'],
  ...overrides,
});

export const evaluation = (overrides: Partial<EvaluationSnapshot> = {}): EvaluationSnapshot => ({
  generated_at: '2024-01-02T00:00:00Z',
  retrieval_precision: 0.9,
  promotion_precision: 0.8,
  wrong_memory_rate: 0.02,
  attribution_accuracy: 0.85,
  latency_ms_p95: 120,
  tokens_per_projection: 500,
  cost_usd: 0.12,
  cache_hit_rate: 0.4,
  ...overrides,
});

export const projection = (overrides: Partial<ProjectionDebug> = {}): ProjectionDebug => ({
  mode: 'focus',
  reason: 'task is sufficiently clear',
  cache_hit: false,
  degraded: false,
  degraded_reasons: [],
  budget: { candidates: 90, ranking: 60, injection_tokens: 4096 },
  usage: { candidates_seen: 2, candidates_ranked: 2, candidates_injected: 1, tokens_injected: 100 },
  candidates: [
    {
      memory_id: 'memory-a',
      included: true,
      score: 0.9,
      reason: { relevance: 0.9, confidence: 0.8, coverage: 0.66, freshness: 1, applicability_hit: false, channels: ['full-text'] },
      provenance: ['full-text'],
    },
    {
      memory_id: 'memory-b',
      included: false,
      score: 0.2,
      excluded_reason: 'cross-session',
      provenance: ['vector'],
    },
  ],
  ...overrides,
});

export function fakeConsoleApi(overrides: Partial<ConsoleApi> = {}): ConsoleApi {
  return {
    listSessions: vi.fn(async () => page([session('s1')])),
    listMemories: vi.fn(async () => page([memory('m1')])),
    listCandidates: vi.fn(async () => page([candidate('c1')])),
    reviewCandidate: vi.fn(async (id: string) => candidate(id)),
    debugProjection: vi.fn(async () => projection()),
    listFailures: vi.fn(async () => page([failure('f1')])),
    getEvaluation: vi.fn(async () => evaluation()),
    listMembers: vi.fn(async () => page([member('u1')])),
    inviteMember: vi.fn(async (email: string) => member(email)),
    updateMemberRole: vi.fn(async (id: string, role: Member['role']) => member(id, { role })),
    listAgents: vi.fn(async () => page([agent('a1')])),
    disableAgent: vi.fn(async (id: string) => agent(id, { status: 'disabled' })),
    ...overrides,
  } as unknown as ConsoleApi;
}