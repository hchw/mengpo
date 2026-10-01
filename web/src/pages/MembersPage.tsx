import { useCallback, useEffect, useState } from 'react';
import type { ConsoleApi } from '../api/console';
import { can, type Permission } from '../api/client';
import type { AgentRecord, Member } from '../api/types';
import { StateView } from '../components/states';

export interface MembersPageProps {
  api: ConsoleApi;
  role: string;
}

// MembersPage manages members, roles and agents. Every mutating action is gated
// by the caller's tenant role, so a plain member sees a permission state rather
// than controls that would fail server-side.
export function MembersPage({ api, role }: MembersPageProps) {
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [members, setMembers] = useState<Member[]>([]);
  const [agents, setAgents] = useState<AgentRecord[]>([]);
  const [nonce, setNonce] = useState(0);

  const load = useCallback(() => {
    setStatus('loading');
    Promise.all([api.listMembers(), api.listAgents()])
      .then(([memberPage, agentPage]) => {
        setMembers(memberPage.items);
        setAgents(agentPage.items);
        setStatus(memberPage.items.length === 0 && agentPage.items.length === 0 ? 'empty' : 'ready');
      })
      .catch((cause: unknown) => {
        setError(cause);
        setStatus('error');
      });
  }, [api]);

  useEffect(() => {
    load();
  }, [load, nonce]);

  const mutate = async (action: () => Promise<unknown>) => {
    try {
      await action();
      setNonce((value) => value + 1);
    } catch (cause) {
      setError(cause);
      setStatus('error');
    }
  };

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} onRetry={() => setNonce((value) => value + 1)} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description="No members or agents in this tenant." />;
  }

  const canManageMembers = can(role, 'member.manage' as Permission);
  const canManageAgents = can(role, 'agent.manage' as Permission);

  return (
    <section aria-labelledby="members-title" className="page page--members">
      <h1 id="members-title">Members &amp; Agents</h1>
      <div className="panel" data-testid="members-panel">
        <h2>Members</h2>
        {!canManageMembers ? <StateView kind="permission" description="Only owners and tenant admins can manage members." /> : null}
        <ul data-testid="member-list">
          {members.map((member) => (
            <li key={member.user_id} data-testid={`member-${member.user_id}`}>
              <span>{member.email}</span>
              <span data-testid={`member-role-${member.user_id}`}>{member.role}</span>
              {canManageMembers ? (
                <label>
                  Role
                  <select
                    aria-label={`role for ${member.email}`}
                    value={member.role}
                    onChange={(event) => void mutate(() => api.updateMemberRole(member.user_id, event.target.value as Member['role']))}
                  >
                    <option value="owner">owner</option>
                    <option value="tenant-admin">tenant-admin</option>
                    <option value="member">member</option>
                  </select>
                </label>
              ) : null}
            </li>
          ))}
        </ul>
      </div>
      <div className="panel" data-testid="agents-panel">
        <h2>Agents</h2>
        {!canManageAgents ? <StateView kind="permission" description="Only owners and tenant admins can manage agents." /> : null}
        <ul data-testid="agent-list">
          {agents.map((agent) => (
            <li key={agent.id} data-testid={`agent-${agent.id}`}>
              <span>{agent.name}</span>
              <span data-testid={`agent-status-${agent.id}`}>{agent.status}</span>
              {canManageAgents && agent.status === 'active' ? (
                <button onClick={() => void mutate(() => api.disableAgent(agent.id))}>disable</button>
              ) : null}
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}