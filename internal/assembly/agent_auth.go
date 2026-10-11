package assembly

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/observation"
)

// AgentCredentialLookup resolves a hashed agent secret to its agent.
type AgentCredentialLookup interface {
	FindByCredentialHash(ctx context.Context, secretHash []byte) (auth.Agent, auth.AgentCredential, error)
}

// AgentAuthenticator resolves an agent Bearer secret to a trusted identity. The
// bound user is read from the request envelope and checked against the agent's
// allowed users, so a credential can never act for an unauthorized principal.
// The envelope body is restored before the request continues.
type AgentAuthenticator struct {
	Agents AgentCredentialLookup
}

func (a AgentAuthenticator) Authenticate(r *http.Request) (agentaccess.Identity, error) {
	if a.Agents == nil {
		return agentaccess.Identity{}, agentaccess.ErrNoIdentity
	}
	secret := bearerValue(r)
	if secret == "" || !strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
		return agentaccess.Identity{}, agentaccess.ErrNoIdentity
	}
	// The envelope is only consulted for the optional bound user; a credential
	// with a single allowed user resolves everything else from the agent.
	peek, _ := peekEnvelope(r)
	digest := sha256.Sum256([]byte(secret))
	agent, credential, err := a.Agents.FindByCredentialHash(r.Context(), digest[:])
	if err != nil || agent.Status != auth.AgentActive || credential.RevokedAt != nil ||
		(!credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(time.Now())) {
		return agentaccess.Identity{}, agentaccess.ErrNoIdentity
	}
	userID := strings.TrimSpace(peek.Scope.UserID)
	if userID == "" {
		if len(agent.AllowedUserIDs) != 1 {
			// Zero users means the agent is unusable; more than one is ambiguous
			// and the caller must declare which bound user it acts for.
			return agentaccess.Identity{}, agentaccess.ErrNoIdentity
		}
		userID = agent.AllowedUserIDs[0]
	}
	if !slices.Contains(agent.AllowedUserIDs, userID) {
		return agentaccess.Identity{}, agentaccess.ErrForbidden
	}
	return agentaccess.Identity{
		TenantID:     agent.TenantID,
		UserID:       userID,
		AgentID:      agent.ID,
		SourceID:     agent.ID,
		SessionID:    peek.Scope.SessionID,
		ScopeType:    peek.Scope.Type,
		Capabilities: agent.Capabilities,
		Source:       agentaccess.SourceAgentContext,
		AccessLevel:  observation.Level1,
	}, nil
}

// CompositeAuthenticator tries the console session first, then an agent
// credential. Both are server-side verifications; neither trusts request claims.
type CompositeAuthenticator struct {
	Session Authenticator
	Agent   Authenticator
}

func (c CompositeAuthenticator) Authenticate(r *http.Request) (agentaccess.Identity, error) {
	if c.Session != nil {
		if identity, err := c.Session.Authenticate(r); err == nil && identity.TenantID != "" {
			return identity, nil
		}
	}
	if c.Agent != nil {
		return c.Agent.Authenticate(r)
	}
	return agentaccess.Identity{}, agentaccess.ErrNoIdentity
}

// envelopeScopePeek is the minimum envelope shape needed to bind an agent to a
// user before the real envelope is decoded by the handler.
type envelopeScopePeek struct {
	Scope struct {
		UserID    string `json:"user_id"`
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
	} `json:"scope"`
}

// peekEnvelope reads the request body to learn the declared user, then restores
// it so the handler can decode the envelope normally.
func peekEnvelope(r *http.Request) (envelopeScopePeek, error) {
	var peek envelopeScopePeek
	if r.Body == nil {
		return peek, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return envelopeScopePeek{}, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return peek, nil
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return envelopeScopePeek{}, err
	}
	return peek, nil
}

func bearerValue(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}
