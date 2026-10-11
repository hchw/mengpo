import { useCallback, useEffect, useState, type FormEvent } from 'react';
import type { ConsoleApi } from '../api/console';
import { can, type Permission } from '../api/client';
import type { AgentRecord, Member, ScopeType } from '../api/types';
import { StateView } from '../components/states';
import { useI18n } from '../i18n';

export interface MembersPageProps {
  api: ConsoleApi;
  role: string;
  currentUserId?: string;
}

const AGENT_SCOPES: ScopeType[] = ['user-global', 'session'];
const AGENT_CAPABILITIES = ['observe', 'project', 'feedback'] as const;

// MembersPage manages members, roles and agents. Every mutating action is gated
// by the caller's tenant role, so a plain member sees a permission state rather
// than controls that would fail server-side.
export function MembersPage({ api, role, currentUserId }: MembersPageProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<'loading' | 'ready' | 'empty' | 'error'>('loading');
  const [error, setError] = useState<unknown>();
  const [members, setMembers] = useState<Member[]>([]);
  const [agents, setAgents] = useState<AgentRecord[]>([]);
  const [nonce, setNonce] = useState(0);

  const [agentName, setAgentName] = useState('');
  const [scopes, setScopes] = useState<ScopeType[]>(['user-global', 'session']);
  const [capabilities, setCapabilities] = useState<string[]>(['observe', 'project', 'feedback']);
  const [secret, setSecret] = useState('');
  const [creating, setCreating] = useState(false);

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

  const submitAgent = async (event: FormEvent) => {
    event.preventDefault();
    if (!agentName.trim() || scopes.length === 0 || capabilities.length === 0) {
      return;
    }
    setCreating(true);
    try {
      const created = await api.createAgent({
        name: agentName.trim(),
        allowed_user_ids: currentUserId ? [currentUserId] : undefined,
        allowed_scopes: scopes,
        capabilities,
      });
      setSecret(created.secret);
      setAgentName('');
      setNonce((value) => value + 1);
    } catch (cause) {
      setError(cause);
      setStatus('error');
    } finally {
      setCreating(false);
    }
  };

  const toggleScope = (scope: ScopeType) =>
    setScopes((current) => (current.includes(scope) ? current.filter((value) => value !== scope) : [...current, scope]));
  const toggleCapability = (capability: string) =>
    setCapabilities((current) => (current.includes(capability) ? current.filter((value) => value !== capability) : [...current, capability]));

  if (status === 'loading') {
    return <StateView kind="loading" />;
  }
  if (status === 'error') {
    return <StateView kind="error" error={error} onRetry={() => setNonce((value) => value + 1)} />;
  }
  if (status === 'empty') {
    return <StateView kind="empty" description={t('members.empty')} />;
  }

  const canManageMembers = can(role, 'member.manage' as Permission);
  const canManageAgents = can(role, 'agent.manage' as Permission);

  return (
    <section aria-labelledby="members-title" className="page page--members">
      <h1 id="members-title">{t('members.title')}</h1>
      <div className="panel" data-testid="members-panel">
        <h2>{t('members.members')}</h2>
        {!canManageMembers ? <StateView kind="permission" description={t('members.onlyAdmins')} /> : null}
        <ul data-testid="member-list">
          {members.map((member) => (
            <li key={member.user_id} data-testid={`member-${member.user_id}`}>
              <span>{member.email}</span>
              <span data-testid={`member-role-${member.user_id}`}>{member.role}</span>
              {canManageMembers ? (
                <label>
                  {t('members.role')}
                  <select
                    aria-label={t('members.roleFor', { email: member.email })}
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
        <h2>{t('members.agents')}</h2>
        {!canManageAgents ? <StateView kind="permission" description={t('members.onlyAdminsAgents')} /> : null}
        {canManageAgents ? (
          <form aria-label={t('members.createAgent')} onSubmit={submitAgent} className="agent-form">
            <label>
              {t('members.agentName')}
              <input aria-label={t('members.agentName')} value={agentName} onChange={(event) => setAgentName(event.target.value)} />
            </label>
            <fieldset>
              <legend>{t('members.agentScopes')}</legend>
              {AGENT_SCOPES.map((scope) => (
                <label key={scope}>
                  <input type="checkbox" aria-label={scope} checked={scopes.includes(scope)} onChange={() => toggleScope(scope)} />
                  {scope}
                </label>
              ))}
            </fieldset>
            <fieldset>
              <legend>{t('members.agentCapabilities')}</legend>
              {AGENT_CAPABILITIES.map((capability) => (
                <label key={capability}>
                  <input
                    type="checkbox"
                    aria-label={capability}
                    checked={capabilities.includes(capability)}
                    onChange={() => toggleCapability(capability)}
                  />
                  {capability}
                </label>
              ))}
            </fieldset>
            <button type="submit" disabled={creating}>
              {creating ? t('members.creatingAgent') : t('members.createAgent')}
            </button>
          </form>
        ) : null}
        {secret ? (
          <p data-testid="agent-secret">
            {t('members.agentSecretNotice')} <code>{secret}</code>
          </p>
        ) : null}
        <ul data-testid="agent-list">
          {agents.map((agent) => (
            <li key={agent.id} data-testid={`agent-${agent.id}`}>
              <span>{agent.name}</span>
              <span data-testid={`agent-status-${agent.id}`}>{agent.status}</span>
              {canManageAgents && agent.status === 'active' ? (
                <button onClick={() => void mutate(() => api.disableAgent(agent.id))}>{t('members.disable')}</button>
              ) : null}
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
