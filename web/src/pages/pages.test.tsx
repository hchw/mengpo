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
import { ApiClientError } from '../api/client';
import { agent, candidate, evaluation, failure, fakeConsoleApi, member, memory, page, projection, session } from '../test/fakes';

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

  it('shows a permission state for a plain member', async () => {
    render(<MembersPage api={fakeConsoleApi()} role="member" />);
    await waitFor(() => expect(screen.getByTestId('member-list')).toBeInTheDocument());
    expect(screen.getAllByText('Permission required').length).toBeGreaterThan(0);
    expect(screen.queryByLabelText('role for u1@example.com')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'disable' })).not.toBeInTheDocument();
  });
});
