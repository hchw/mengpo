package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/domain/auth"
)

// AgentRepository persists tenant-bound agents and their credential verifiers.
type AgentRepository struct{ db *sql.DB }

func NewAgentRepository(db *sql.DB) *AgentRepository { return &AgentRepository{db: db} }

// CreateAgent inserts the agent and its first credential in one transaction.
func (r *AgentRepository) CreateAgent(ctx context.Context, agent auth.Agent, credential auth.AgentCredential) error {
	if agent.ID == "" || agent.TenantID == "" || credential.ID == "" || len(credential.SecretHash) == 0 {
		return errors.New("invalid agent registration")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent registration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.agents (id, tenant_id, name, allowed_user_ids, allowed_session_ids, allowed_scopes, capabilities, status)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid[], $5::uuid[], $6::text[], $7::text[], $8)`,
		agent.ID, agent.TenantID, agent.Name, uuidArrayLiteral(agent.AllowedUserIDs), uuidArrayLiteral(agent.AllowedSessionIDs),
		uuidArrayLiteral(agent.AllowedScopes), uuidArrayLiteral(agent.Capabilities), agentStatus(agent.Status)); err != nil {
		return fmt.Errorf("insert agent: %w", err)
	}
	if err := insertCredential(ctx, tx, credential); err != nil {
		return err
	}
	return tx.Commit()
}

// RotateCredential revokes the existing verifier and stores the new one atomically.
func (r *AgentRepository) RotateCredential(ctx context.Context, agentID string, credential auth.AgentCredential) error {
	if agentID == "" || len(credential.SecretHash) == 0 {
		return errors.New("invalid credential rotation")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential rotation: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE public.agent_credentials SET revoked_at = now() WHERE agent_id = $1::uuid AND revoked_at IS NULL`, agentID); err != nil {
		return fmt.Errorf("revoke agent credential: %w", err)
	}
	if err := insertCredential(ctx, tx, credential); err != nil {
		return err
	}
	return tx.Commit()
}

func insertCredential(ctx context.Context, tx *sql.Tx, credential auth.AgentCredential) error {
	var expiresAt any
	if !credential.ExpiresAt.IsZero() {
		expiresAt = credential.ExpiresAt
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.agent_credentials (id, agent_id, tenant_id, secret_hash, expires_at)
		VALUES ($1, $2::uuid, $3::uuid, $4, $5)`,
		credential.ID, credential.AgentID, credential.TenantID, credential.SecretHash, expiresAt); err != nil {
		return fmt.Errorf("insert agent credential: %w", err)
	}
	return nil
}

// FindByCredentialHash resolves an active, unexpired credential to its agent.
func (r *AgentRepository) FindByCredentialHash(ctx context.Context, secretHash []byte) (auth.Agent, auth.AgentCredential, error) {
	var agent auth.Agent
	var credential auth.AgentCredential
	var allowedUsersJSON, allowedSessionsJSON, allowedScopesJSON, capabilitiesJSON string
	var revokedAt sql.NullTime
	var expiresAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT a.id::text, a.tenant_id::text, a.name, to_json(a.allowed_user_ids)::text, to_json(a.allowed_session_ids)::text, to_json(a.allowed_scopes)::text, to_json(a.capabilities)::text, a.status, a.created_at, a.updated_at,
		       c.id, c.agent_id::text, c.tenant_id::text, c.secret_hash, c.expires_at, c.revoked_at, c.created_at
		FROM public.agents a
		JOIN public.agent_credentials c ON c.agent_id = a.id
		WHERE c.secret_hash = $1 AND c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > now())`, secretHash).
		Scan(&agent.ID, &agent.TenantID, &agent.Name, &allowedUsersJSON, &allowedSessionsJSON, &allowedScopesJSON, &capabilitiesJSON, &agent.Status, &agent.CreatedAt, &agent.UpdatedAt,
			&credential.ID, &credential.AgentID, &credential.TenantID, &credential.SecretHash, &expiresAt, &revokedAt, &credential.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Agent{}, auth.AgentCredential{}, errNoAgentCredential
	}
	if err != nil {
		return auth.Agent{}, auth.AgentCredential{}, fmt.Errorf("find agent credential: %w", err)
	}
	agent.AllowedUserIDs = parseStringArray(allowedUsersJSON)
	agent.AllowedSessionIDs = parseStringArray(allowedSessionsJSON)
	agent.AllowedScopes = parseStringArray(allowedScopesJSON)
	agent.Capabilities = parseStringArray(capabilitiesJSON)
	if expiresAt.Valid {
		credential.ExpiresAt = expiresAt.Time
	}
	if revokedAt.Valid {
		credential.RevokedAt = &revokedAt.Time
	}
	return agent, credential, nil
}

// DisableAgent disables an agent and revokes every credential.
func (r *AgentRepository) DisableAgent(ctx context.Context, agentID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin agent disable: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE public.agents SET status = 'disabled', updated_at = now() WHERE id = $1::uuid`, agentID); err != nil {
		return fmt.Errorf("disable agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.agent_credentials SET revoked_at = now() WHERE agent_id = $1::uuid AND revoked_at IS NULL`, agentID); err != nil {
		return fmt.Errorf("revoke disabled agent credentials: %w", err)
	}
	return tx.Commit()
}

// ListAgents lists every agent for one tenant.
func (r *AgentRepository) ListAgents(ctx context.Context, tenantID string) ([]auth.Agent, error) {
	if tenantID == "" {
		return nil, errors.New("tenant id is required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id::text, tenant_id::text, name, to_json(allowed_user_ids)::text, to_json(allowed_session_ids)::text, to_json(allowed_scopes)::text, to_json(capabilities)::text, status, created_at, updated_at
		FROM public.agents WHERE tenant_id = $1::uuid ORDER BY created_at, id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	var agents []auth.Agent
	for rows.Next() {
		var agent auth.Agent
		var allowedUsersJSON, allowedSessionsJSON, allowedScopesJSON, capabilitiesJSON string
		if err := rows.Scan(&agent.ID, &agent.TenantID, &agent.Name, &allowedUsersJSON, &allowedSessionsJSON, &allowedScopesJSON, &capabilitiesJSON, &agent.Status, &agent.CreatedAt, &agent.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agent.AllowedUserIDs = parseStringArray(allowedUsersJSON)
		agent.AllowedSessionIDs = parseStringArray(allowedSessionsJSON)
		agent.AllowedScopes = parseStringArray(allowedScopesJSON)
		agent.Capabilities = parseStringArray(capabilitiesJSON)
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

// parseStringArray decodes a JSON array of strings produced by to_json.
func parseStringArray(value string) []string {
	if value == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(value), &out); err != nil {
		return nil
	}
	return out
}

var errNoAgentCredential = errors.New("agent credential not found")

func agentStatus(status auth.AgentStatus) auth.AgentStatus {
	if status == "" {
		return auth.AgentActive
	}
	return status
}
