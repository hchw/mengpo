import type { ApiClient } from './client';
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
} from './types';
import type { AnalysisRun, ProviderTestResult, ProviderUpdate, ProviderView, ScheduleStatus, ScheduleUpdate } from './types';

export const ConsolePaths = {
  sessions: '/api/v1/sessions/list',
  memories: '/api/v1/memories',
  candidates: '/api/v1/candidates',
  project: '/api/v1/project',
  failures: '/api/v1/failures',
  evaluation: '/api/v1/evaluation',
  members: '/api/v1/members',
  agents: '/api/v1/agents', providers: '/api/v1/providers', providerTest: '/api/v1/providers/test', schedules: '/api/v1/schedules', analysisRuns: '/api/v1/analysis-runs',
} as const;

// ConsoleApi is the tenant-scoped read/write surface the console pages use. It
// wraps ApiClient so every request inherits the active tenant and scope.
export class ConsoleApi {
  constructor(
    private readonly client: ApiClient,
    private readonly principal: { type: 'user' | 'agent'; id: string },
  ) {}

  private scope(sessionId?: string) {
    return sessionId
      ? ({ user_id: this.principal.id, type: 'session', session_id: sessionId } as const)
      : ({ user_id: this.principal.id, type: 'user-global' } as const);
  }

  listSessions(page = 1, pageSize = 20): Promise<Page<SessionSummary>> {
    return this.client.list<SessionSummary>(ConsolePaths.sessions, { principal: this.principal, scope: this.scope() }, page, pageSize);
  }

  listMemories(page = 1, pageSize = 20, sessionId?: string): Promise<Page<MemoryRecord>> {
    return this.client.list<MemoryRecord>(ConsolePaths.memories, { principal: this.principal, scope: this.scope(sessionId) }, page, pageSize);
  }

  listCandidates(page = 1, pageSize = 20): Promise<Page<CandidateRecord>> {
    return this.client.list<CandidateRecord>(ConsolePaths.candidates, { principal: this.principal, scope: this.scope() }, page, pageSize);
  }

  reviewCandidate(candidateId: string, action: 'confirm' | 'reject' | 'correct' | 'merge' | 'expire' | 'delete', payload: Record<string, unknown> = {}): Promise<CandidateRecord> {
    return this.client.request<CandidateRecord>(`${ConsolePaths.candidates}/${candidateId}/${action}`, { principal: this.principal, scope: this.scope() }, payload);
  }

  debugProjection(query: string, sessionId?: string): Promise<ProjectionDebug> {
    return this.client.request<ProjectionDebug>(ConsolePaths.project, { principal: this.principal, scope: this.scope(sessionId) }, { query });
  }

  listFailures(page = 1, pageSize = 20): Promise<Page<FailureRecord>> {
    return this.client.list<FailureRecord>(ConsolePaths.failures, { principal: this.principal, scope: this.scope() }, page, pageSize);
  }

  getEvaluation(): Promise<EvaluationSnapshot> {
    return this.client.request<EvaluationSnapshot>(ConsolePaths.evaluation, { principal: this.principal, scope: this.scope() });
  }

  listMembers(page = 1, pageSize = 20): Promise<Page<Member>> {
    return this.client.list<Member>(ConsolePaths.members, { principal: this.principal, scope: this.scope() }, page, pageSize);
  }

  inviteMember(email: string, role: Member['role']): Promise<Member> {
    return this.client.request<Member>(ConsolePaths.members, { principal: this.principal, scope: this.scope() }, { email, role });
  }

  updateMemberRole(userId: string, role: Member['role']): Promise<Member> {
    return this.client.request<Member>(`${ConsolePaths.members}/${userId}`, { principal: this.principal, scope: this.scope() }, { role });
  }

  listAgents(page = 1, pageSize = 20): Promise<Page<AgentRecord>> {
    return this.client.list<AgentRecord>(ConsolePaths.agents, { principal: this.principal, scope: this.scope() }, page, pageSize);
  }

  disableAgent(agentId: string): Promise<AgentRecord> {
    return this.client.request<AgentRecord>(`${ConsolePaths.agents}/${agentId}/disable`, { principal: this.principal, scope: this.scope() });
  }

  getProvider(): Promise<ProviderView> {
    return this.client.request<ProviderView>(ConsolePaths.providers, { principal: this.principal, scope: this.scope() });
  }
  updateProvider(input: ProviderUpdate): Promise<ProviderView> {
    return this.client.request<ProviderView>(ConsolePaths.providers, { principal: this.principal, scope: this.scope() }, input, 'PUT');
  }
  testProvider(input: ProviderUpdate): Promise<ProviderTestResult> {
    return this.client.request<ProviderTestResult>(ConsolePaths.providerTest, { principal: this.principal, scope: this.scope() }, input);
  }
  listSchedules(): Promise<{ items: ScheduleStatus[] }> {
    return this.client.request<{ items: ScheduleStatus[] }>(ConsolePaths.schedules, { principal: this.principal, scope: this.scope() });
  }
  updateSchedule(input: ScheduleUpdate): Promise<unknown> {
    return this.client.request(ConsolePaths.schedules, { principal: this.principal, scope: this.scope() }, input, 'PUT');
  }
  listAnalysisRuns(page = 1, pageSize = 20): Promise<{ items: AnalysisRun[] }> {
    return this.client.request<{ items: AnalysisRun[] }>(ConsolePaths.analysisRuns, { principal: this.principal, scope: this.scope() }, { page, page_size: pageSize });
  }
}
