import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DashboardPage } from './DashboardPage';
import { SessionExplorerPage } from './SessionExplorerPage';
import { BackgroundMemoryPage } from './BackgroundMemoryPage';
import { CandidateReviewPage } from './CandidateReviewPage';
import { ProjectionDebuggerPage } from './ProjectionDebuggerPage';
import { FailureAnalysisPage } from './FailureAnalysisPage';
import { MemoryEvaluationPage } from './MemoryEvaluationPage';
import { MembersPage } from './MembersPage';
import { SettingsPage } from './SettingsPage';
import { AnalysisRunsPage } from './AnalysisRunsPage';
import { ApiClientError } from '../api/client';
import { LanguageProvider } from '../i18n';
import { agent, analysisRun, analysisRunDetail, candidate, evaluation, failure, fakeConsoleApi, member, memory, page, projection, provider, scheduleStatus, session } from '../test/fakes';

const user = userEvent.setup();

describe('DashboardPage', () => {
  it('shows session, memory and evaluation metrics', async () => {
    render(<DashboardPage api={fakeConsoleApi({ listSessions: async () => page([session('s1'), session('s2')]), listMemories: async () => page([memory('m1')]) })} />);
    await waitFor(() => expect(screen.getByTestId('session-count').textContent).toBe('2'));
    expect(screen.getByTestId('memory-count').textContent).toBe('1');
    expect(screen.getByTestId('retrieval-precision').textContent).toContain('90.0%');
  });

  it('renders an empty state when the tenant has no data', async () => {
    render(<DashboardPage api={fakeConsoleApi({ listSessions: async () => page([]), listMemories: async () => page([]) })} />);
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('No sessions or memories'));
  });

  it('reports unmeasured evaluation metrics as no data, not 0%', async () => {
    render(<DashboardPage api={fakeConsoleApi({ getEvaluation: async () => evaluation({ has_data: false }) })} />);
    await waitFor(() => expect(screen.getByTestId('retrieval-precision')).toBeTruthy());
    expect(screen.getByTestId('retrieval-precision').textContent).toContain('no data yet');
    expect(screen.getByTestId('cache-hit-rate').textContent).toContain('no data yet');
  });
});

describe('SessionExplorerPage', () => {
  it('lists sessions and loads scoped memories for the selection', async () => {
    const api = fakeConsoleApi({
      listSessions: async () => page([session('s1'), session('s2')]),
      listMemories: async (_p, _s, sessionId) => page([memory(`m-${sessionId}`)]),
    });
    render(<SessionExplorerPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('session-list').children.length).toBe(2));
    await waitFor(() => expect(screen.getByTestId('session-memories').textContent).toContain('m-s1'));
    await user.click(screen.getByText('Session s2'));
    await waitFor(() => expect(screen.getByTestId('session-memories').textContent).toContain('m-s2'));
  });

  it('paginates sessions using the server-reported total', async () => {
    const listSessions = vi.fn(async (p = 1) => page([session(`s${p}`)], { total: 40, page: p, page_size: 20 }));
    const api = fakeConsoleApi({
      listSessions,
      listMemories: async () => page([memory('m1')]),
    });
    render(<SessionExplorerPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('session-list').children.length).toBe(1));
    expect(screen.getByTestId('pagination-status').textContent).toContain('Page 1 of 2');
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(screen.getByTestId('pagination-status').textContent).toContain('Page 2 of 2'));
    expect(listSessions).toHaveBeenCalledWith(2, 20);
    expect(screen.getByRole('button', { name: 'Next' })).toBeDisabled();
  });
});

describe('BackgroundMemoryPage', () => {
  it('filters by status and marks stale memories', async () => {
    const stale = memory('m-stale', { status: 'active', updated_at: '2000-01-01T00:00:00Z' });
    const conflicted = memory('m-conflict', { status: 'conflicted' });
    render(<BackgroundMemoryPage api={fakeConsoleApi({ listMemories: async () => page([stale, conflicted]) })} now={() => new Date('2024-01-02T00:00:00Z').getTime()} />);
    await waitFor(() => expect(screen.getByTestId('memory-list').children.length).toBe(2));
    expect(screen.getByTestId('memory-stale-m-stale')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Conflicted' }));
    await waitFor(() => expect(screen.getByTestId('memory-list').children.length).toBe(1));
    expect(screen.queryByTestId('memory-m-stale')).not.toBeInTheDocument();
  });
});

describe('CandidateReviewPage', () => {
  it('optimistically removes a confirmed candidate', async () => {
    const api = fakeConsoleApi({ listCandidates: async () => page([candidate('c1'), candidate('c2')]) });
    render(<CandidateReviewPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('candidate-list').children.length).toBe(2));
    const confirmButtons = screen.getAllByRole('button', { name: 'confirm' });
    await user.click(confirmButtons[0]);
    await waitFor(() => expect(screen.queryByTestId('candidate-c1')).not.toBeInTheDocument());
    expect(api.reviewCandidate).toHaveBeenCalledWith('c1', 'confirm', {});
  });

  it('rolls back and shows a conflict when the decision conflicts', async () => {
    const api = fakeConsoleApi({
      listCandidates: async () => page([candidate('c1')]),
      reviewCandidate: async () => {
        throw new ApiClientError('stale version', { code: 'CONFLICT', status: 409, retryable: false });
      },
    });
    render(<CandidateReviewPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('candidate-c1')).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: 'merge' }));
    await waitFor(() => expect(screen.getByText('Conflicting change')).toBeInTheDocument());
    expect(screen.getByTestId('candidate-c1')).toBeInTheDocument();
  });
});

describe('ProjectionDebuggerPage', () => {
  it('renders candidates, budget and degradation', async () => {
    const api = fakeConsoleApi({
      debugProjection: async () => projection({ degraded: true, degraded_reasons: ['embedding-unavailable'] }),
    });
    render(<ProjectionDebuggerPage api={api} initialQuery="tenant routing" />);
    await user.click(screen.getByRole('button', { name: 'Run projection' }));
    await waitFor(() => expect(screen.getByTestId('projection-debug')).toBeInTheDocument());
    expect(screen.getByTestId('debug-mode').textContent).toBe('focus');
    expect(screen.getByTestId('debug-budget').textContent).toContain('candidates=90');
    expect(screen.getByTestId('debug-degraded').textContent).toContain('embedding-unavailable');
    expect(screen.getByTestId('debug-candidate-memory-b').textContent).toContain('cross-session');
  });
});

describe('FailureAnalysisPage', () => {
  it('separates confidence from attribution completeness', async () => {
    const api = fakeConsoleApi({
      listFailures: async () => page([failure('f1', { confidence: 'confirmed', attribution: 'direct' }), failure('f2', { confidence: 'suspected', attribution: 'unknown' })]),
    });
    render(<FailureAnalysisPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('failure-list').children.length).toBe(2));
    await user.click(screen.getByRole('button', { name: 'suspected' }));
    await waitFor(() => expect(screen.getByTestId('failure-list').children.length).toBe(1));
    expect(screen.getByTestId('attribution-count-direct').textContent).toContain('1');
    expect(screen.getByTestId('attribution-count-unknown').textContent).toContain('1');
  });
});

describe('MemoryEvaluationPage', () => {
  it('formats evaluation metrics', async () => {
    render(<MemoryEvaluationPage api={fakeConsoleApi({ getEvaluation: async () => evaluation() })} />);
    await waitFor(() => expect(screen.getByTestId('metric-retrieval_precision').textContent).toBe('90.0%'));
    expect(screen.getByTestId('metric-latency_ms_p95').textContent).toBe('120 ms');
    expect(screen.getByTestId('metric-cost_usd').textContent).toBe('$0.12');
  });

  it('shows a no-data notice when nothing has been evaluated', async () => {
    render(<MemoryEvaluationPage api={fakeConsoleApi({ getEvaluation: async () => evaluation({ has_data: false }) })} />);
    await waitFor(() => expect(screen.getByTestId('evaluation-no-data')).toBeTruthy());
    expect(screen.getByTestId('metric-retrieval_precision').textContent).toBe('—');
  });

  it('renders metrics outside measured_metrics as unmeasured', async () => {
    render(
      <MemoryEvaluationPage
        api={fakeConsoleApi({ getEvaluation: async () => evaluation({ measured_metrics: ['retrieval_precision', 'cache_hit_rate'] }) })}
      />,
    );
    await waitFor(() => expect(screen.getByTestId('metric-retrieval_precision').textContent).toBe('90.0%'));
    expect(screen.getByTestId('metric-promotion_precision').textContent).toBe('—');
    expect(screen.getByTestId('metric-cost_usd').textContent).toBe('—');
  });
});

describe('MembersPage', () => {
  it('lets a tenant admin manage members and agents', async () => {
    const updateMemberRole = vi.fn(async (id: string, role: 'member' | 'owner' | 'tenant-admin') => member(id, { role }));
    const disableAgent = vi.fn(async (id: string) => agent(id, { status: 'disabled' }));
    const api = fakeConsoleApi({ listMembers: async () => page([member('u1', { email: 'a@example.com' })]), listAgents: async () => page([agent('a1')]), updateMemberRole, disableAgent });
    render(<MembersPage api={api} role="tenant-admin" />);
    await waitFor(() => expect(screen.getByTestId('member-u1')).toBeInTheDocument());
    await user.selectOptions(screen.getByLabelText('role for a@example.com'), 'owner');
    await waitFor(() => expect(updateMemberRole).toHaveBeenCalledWith('u1', 'owner'));
    await user.click(screen.getByRole('button', { name: 'disable' }));
    await waitFor(() => expect(disableAgent).toHaveBeenCalledWith('a1'));
  });

  it('creates an agent and reveals the one-time secret', async () => {
    const createAgent = vi.fn(async (input: { name: string; allowed_scopes: string[]; capabilities: string[]; allowed_user_ids?: string[] }) => ({
      id: 'a2',
      name: input.name,
      status: 'active' as const,
      capabilities: input.capabilities,
      allowed_scopes: input.allowed_scopes as ('user-global' | 'session')[],
      secret: 'once-secret',
    }));
    const api = fakeConsoleApi({ listMembers: async () => page([member('u1')]), listAgents: async () => page([]), createAgent });
    render(<MembersPage api={api} role="tenant-admin" currentUserId="u1" />);
    await waitFor(() => expect(screen.getByTestId('agent-list')).toBeInTheDocument());
    await user.type(screen.getByLabelText('Name'), 'Pi Agent');
    await user.click(screen.getByRole('button', { name: 'Create agent' }));
    await waitFor(() => expect(createAgent).toHaveBeenCalledWith(expect.objectContaining({ name: 'Pi Agent', allowed_user_ids: ['u1'] })));
    expect(screen.getByTestId('agent-secret').textContent).toContain('once-secret');
  });

  it('shows a permission state for a plain member', async () => {
    render(<MembersPage api={fakeConsoleApi()} role="member" />);
    await waitFor(() => expect(screen.getByTestId('member-list')).toBeInTheDocument());
    expect(screen.getAllByText('Permission required').length).toBeGreaterThan(0);
    expect(screen.queryByLabelText('role for u1@example.com')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'disable' })).not.toBeInTheDocument();
  });
});

describe('SettingsPage providers', () => {
  it('lets an administrator save and test the provider without echoing the secret', async () => {
    const api = fakeConsoleApi({
      getProvider: vi.fn(async () => provider({ enabled: true, base_url: 'https://llm.example', model: 'm', has_secret: true, secret_hint: '****cret', source: 'tenant' })),
    });
    render(<SettingsPage api={api} role="owner" tenant={{ id: 't1', name: 'Demo', status: 'active', role: 'owner' }} />);
    await waitFor(() => expect(screen.getByTestId('provider-secret').textContent).toBe('****cret'));
    expect(screen.getByTestId('provider-source').textContent).toBe('tenant');
    await user.click(screen.getByRole('button', { name: 'Test connection' }));
    await waitFor(() => expect(screen.getByTestId('provider-test-status').textContent).toContain('ok'));
    await user.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.getByTestId('provider-save-status').textContent).toBe('saved'));
    expect(api.updateProvider).toHaveBeenCalled();
  });

  it('hides the provider form from non-administrators', async () => {
    render(<SettingsPage api={fakeConsoleApi()} role="member" tenant={{ id: 't1', name: 'Demo', status: 'active', role: 'member' }} />);
    await waitFor(() => expect(screen.getByTestId('provider-readonly')).toBeInTheDocument());
    expect(screen.queryByLabelText('api_key')).not.toBeInTheDocument();
  });
});

describe('AnalysisRunsPage', () => {
  it('shows the tenant curation schedule and run records', async () => {
    const api = fakeConsoleApi({
      listSchedules: vi.fn(async () => ({ items: [scheduleStatus()] })),
      listAnalysisRuns: vi.fn(async () => ({ items: [analysisRun('run-1'), analysisRun('run-2', { status: 'dead_letter', degraded_reason: 'rules_only' })], total: 2, page: 1, page_size: 20 })),
    });
    render(<AnalysisRunsPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('analysis-runs').children.length).toBeGreaterThan(0));
    expect(screen.getByTestId('schedule-cadence').textContent).toBe('86400s');
    expect(screen.getByTestId('schedule-status').textContent).toBe('ok');
    expect(screen.getByTestId('analysis-run-run-1').textContent).toContain('Consolidate memory');
    expect(screen.getByTestId('analysis-run-run-2').textContent).toContain('rules only');
    expect(screen.getByTestId('curation-schedule').dataset.failing).toBe('false');
  });

  it('pages the curation record and requests the next page', async () => {
    const listAnalysisRuns = vi.fn(async (page = 1) => ({ items: [analysisRun(`run-${page}`)], total: 25, page, page_size: 20 }));
    const api = fakeConsoleApi({ listSchedules: vi.fn(async () => ({ items: [scheduleStatus()] })), listAnalysisRuns });
    render(<AnalysisRunsPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('analysis-run-run-1')).toBeInTheDocument());
    expect(screen.getByTestId('pagination-status').textContent).toBe('Page 1 of 2 · 25 total');
    await user.click(screen.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(listAnalysisRuns).toHaveBeenLastCalledWith(2, 20));
    await waitFor(() => expect(screen.getByTestId('analysis-run-run-2')).toBeInTheDocument());
  });

  it('renders curation enums in Chinese when the language is zh', async () => {
    const api = fakeConsoleApi({
      listSchedules: vi.fn(async () => ({ items: [scheduleStatus()] })),
      listAnalysisRuns: vi.fn(async () => ({ items: [analysisRun('run-1'), analysisRun('run-2', { status: 'dead_letter', degraded_reason: 'rules_only' })], total: 2, page: 1, page_size: 20 })),
    });
    render(
      <LanguageProvider initialLanguage="zh">
        <AnalysisRunsPage api={api} />
      </LanguageProvider>,
    );
    await waitFor(() => expect(screen.getByTestId('analysis-runs').children.length).toBeGreaterThan(0));
    expect(screen.getByTestId('schedule-status').textContent).toBe('正常');
    const first = screen.getByTestId('analysis-run-run-1').textContent ?? '';
    expect(first).toContain('记忆固化');
    expect(first).toContain('计划');
    expect(first).toContain('成功');
    expect(first).not.toContain('consolidate_memory');
    const second = screen.getByTestId('analysis-run-run-2').textContent ?? '';
    expect(second).toContain('死信');
    expect(second).toContain('仅规则');
    expect(second).not.toContain('rules_only');
  });

  it('highlights a failing schedule', async () => {
    const api = fakeConsoleApi({
      listSchedules: vi.fn(async () => ({ items: [scheduleStatus({ last_status: 'error', last_error: 'provider unavailable' })] })),
    });
    render(<AnalysisRunsPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('curation-schedule').dataset.failing).toBe('true'));
    expect(screen.getByTestId('schedule-status').textContent).toContain('provider unavailable');
  });

  it('opens a task detail dialog with its source events and produced memories', async () => {
    const api = fakeConsoleApi({
      listSchedules: vi.fn(async () => ({ items: [scheduleStatus()] })),
      listAnalysisRuns: vi.fn(async () => ({ items: [analysisRun('run-1')], total: 1, page: 1, page_size: 20 })),
      getAnalysisRunDetail: vi.fn(async (id: string) => analysisRunDetail(id)),
    });
    render(<AnalysisRunsPage api={api} />);
    await waitFor(() => expect(screen.getByTestId('analysis-run-run-1')).toBeTruthy());

    await user.click(screen.getByRole('button', { name: /Consolidate memory/i }));
    await waitFor(() => expect(screen.getByTestId('run-detail')).toBeTruthy());
    expect(screen.getByTestId('run-detail-meta').textContent).toContain('Prompt version');
    expect(screen.getByTestId('run-detail-inputs').textContent).toContain('tool.failure');
    expect(screen.getByTestId('run-detail-inputs').textContent).toContain('boom');
    expect(screen.getByTestId('run-detail-outputs').textContent).toContain('boom on retry');
    expect(api.getAnalysisRunDetail).toHaveBeenCalledWith('run-1');

    await user.click(screen.getByRole('button', { name: 'Close' }));
    expect(screen.queryByTestId('run-detail')).toBeNull();
  });
});
