package agents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
)

const (
	ProtocolV1            = "memory.v1"
	ScopeUserGlobal       = "user-global"
	ScopeSession          = "session"
	PermissionManageAgent = "agent.manage"
)

var (
	ErrForbidden           = errors.New("agent operation forbidden")
	ErrInvalidAgent        = errors.New("agent registration or credential is invalid")
	ErrInvalidHandshake    = errors.New("agent handshake is invalid")
	ErrUnsupportedProtocol = errors.New("unsupported agent protocol")
)

var supportedCapabilities = []string{"observe", "project", "feedback"}

// Repository operations that update credentials must be atomic: rotating a
// credential revokes the previous verifier and stores the new verifier together.
type Repository interface {
	CreateAgent(ctx context.Context, agent auth.Agent, credential auth.AgentCredential) error
	RotateCredential(ctx context.Context, agentID string, credential auth.AgentCredential) error
	FindByCredentialHash(ctx context.Context, secretHash []byte) (auth.Agent, auth.AgentCredential, error)
	DisableAgent(ctx context.Context, agentID string) error
}

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, clock: time.Now}
}

type RegisterRequest struct {
	Name              string
	AllowedUserIDs    []string
	AllowedSessionIDs []string
	AllowedScopes     []string
	Capabilities      []string
}

// Register creates an Agent under the tenant from a verified ActiveTenantContext.
// The raw credential is returned only once; only its SHA-256 verifier is stored.
func (s *Service) Register(ctx context.Context, tenantContext tenants.ActiveTenantContext, request RegisterRequest) (auth.Agent, string, error) {
	if tenantContext.UserID == "" || tenantContext.Tenant.ID == "" || tenantContext.Tenant.Status != auth.TenantActive ||
		!slices.Contains(tenantContext.Permissions, PermissionManageAgent) || request.Name == "" {
		return auth.Agent{}, "", ErrForbidden
	}
	if !validScopes(request.AllowedScopes) || !validCapabilities(request.Capabilities) {
		return auth.Agent{}, "", ErrInvalidAgent
	}
	agentID, err := newID()
	if err != nil {
		return auth.Agent{}, "", err
	}
	secret, err := newSecret()
	if err != nil {
		return auth.Agent{}, "", err
	}
	now := s.clock()
	agent := auth.Agent{
		ID:                agentID,
		TenantID:          tenantContext.Tenant.ID,
		Name:              request.Name,
		AllowedUserIDs:    uniqueCopy(request.AllowedUserIDs),
		AllowedSessionIDs: uniqueCopy(request.AllowedSessionIDs),
		AllowedScopes:     uniqueCopy(request.AllowedScopes),
		Capabilities:      uniqueCopy(request.Capabilities),
		Status:            auth.AgentActive,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	credential := auth.AgentCredential{
		ID:         "cred_" + agentID,
		AgentID:    agent.ID,
		TenantID:   agent.TenantID,
		SecretHash: hashSecret(secret),
		CreatedAt:  now,
	}
	if err := s.repository.CreateAgent(ctx, agent, credential); err != nil {
		return auth.Agent{}, "", err
	}
	return agent, secret, nil
}

// RotateCredential invalidates the existing token and returns a replacement once.
func (s *Service) RotateCredential(ctx context.Context, tenantContext tenants.ActiveTenantContext, agentID string) (string, error) {
	if tenantContext.UserID == "" || tenantContext.Tenant.ID == "" ||
		!slices.Contains(tenantContext.Permissions, PermissionManageAgent) || agentID == "" {
		return "", ErrForbidden
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	now := s.clock()
	credential := auth.AgentCredential{
		ID:         "cred_" + agentID + "_" + now.UTC().Format("20060102150405.000000000"),
		AgentID:    agentID,
		TenantID:   tenantContext.Tenant.ID,
		SecretHash: hashSecret(secret),
		CreatedAt:  now,
	}
	if err := s.repository.RotateCredential(ctx, agentID, credential); err != nil {
		return "", err
	}
	return secret, nil
}

// DisableAgent stops an Agent from handshaking and revokes its credentials. A
// disabled Agent is rejected at handshake time by the agent status check.
func (s *Service) DisableAgent(ctx context.Context, tenantContext tenants.ActiveTenantContext, agentID string) error {
	if tenantContext.UserID == "" || tenantContext.Tenant.ID == "" ||
		!slices.Contains(tenantContext.Permissions, PermissionManageAgent) || agentID == "" {
		return ErrForbidden
	}
	if err := s.repository.DisableAgent(ctx, agentID); err != nil {
		return err
	}
	return nil
}

type HandshakeRequest struct {
	Credential            string
	ProtocolVersion       string
	UserID                string
	SessionID             string
	ScopeType             string
	RequestedCapabilities []string
}

type HandshakeResponse struct {
	ProtocolVersion     string   `json:"protocol_version"`
	AgentID             string   `json:"agent_id"`
	TenantID            string   `json:"tenant_id"`
	AllowedCapabilities []string `json:"allowed_capabilities"`
	AllowedEventTypes   []string `json:"allowed_event_types"`
	AllowedSessionIDs   []string `json:"allowed_session_ids"`
	AllowedScopeTypes   []string `json:"allowed_scope_types"`
	MaxPayload          int      `json:"max_payload"`
	PrivacyPolicy       string   `json:"privacy_policy"`
}

func (s *Service) Handshake(ctx context.Context, request HandshakeRequest) (HandshakeResponse, error) {
	if request.ProtocolVersion != ProtocolV1 {
		return HandshakeResponse{}, ErrUnsupportedProtocol
	}
	if request.Credential == "" || request.UserID == "" ||
		(request.ScopeType != ScopeUserGlobal && request.ScopeType != ScopeSession) ||
		(request.ScopeType == ScopeSession && request.SessionID == "") {
		return HandshakeResponse{}, ErrInvalidHandshake
	}
	agent, credential, err := s.repository.FindByCredentialHash(ctx, hashSecret(request.Credential))
	if err != nil || credential.AgentID != agent.ID || credential.TenantID != agent.TenantID ||
		credential.RevokedAt != nil || (!credential.ExpiresAt.IsZero() && !credential.ExpiresAt.After(s.clock())) ||
		agent.Status != auth.AgentActive {
		return HandshakeResponse{}, ErrInvalidHandshake
	}
	if !slices.Contains(agent.AllowedUserIDs, request.UserID) || !slices.Contains(agent.AllowedScopes, request.ScopeType) {
		return HandshakeResponse{}, ErrInvalidHandshake
	}
	if request.ScopeType == ScopeSession && !slices.Contains(agent.AllowedSessionIDs, request.SessionID) {
		return HandshakeResponse{}, ErrInvalidHandshake
	}

	capabilities := make([]string, 0, len(agent.Capabilities))
	for _, requested := range request.RequestedCapabilities {
		if !slices.Contains(supportedCapabilities, requested) || !slices.Contains(agent.Capabilities, requested) {
			return HandshakeResponse{}, ErrInvalidHandshake
		}
		capabilities = append(capabilities, requested)
	}
	if len(request.RequestedCapabilities) == 0 {
		for _, configured := range agent.Capabilities {
			if slices.Contains(supportedCapabilities, configured) {
				capabilities = append(capabilities, configured)
			}
		}
	}
	capabilities = uniqueCopy(capabilities)
	eventTypes := make([]string, 0)
	if slices.Contains(capabilities, "observe") {
		eventTypes = append(eventTypes, "user_message", "agent_message", "tool_call", "tool_result", "workflow_event")
	}
	if slices.Contains(capabilities, "feedback") {
		eventTypes = append(eventTypes, "feedback")
	}

	return HandshakeResponse{
		ProtocolVersion:     ProtocolV1,
		AgentID:             agent.ID,
		TenantID:            agent.TenantID,
		AllowedCapabilities: capabilities,
		AllowedEventTypes:   eventTypes,
		AllowedSessionIDs:   append([]string(nil), agent.AllowedSessionIDs...),
		AllowedScopeTypes:   append([]string(nil), agent.AllowedScopes...),
		MaxPayload:          1 << 20,
		PrivacyPolicy:       "tenant-policy",
	}, nil
}

func validScopes(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	for _, scope := range scopes {
		if scope != ScopeUserGlobal && scope != ScopeSession {
			return false
		}
	}
	return true
}

func validCapabilities(capabilities []string) bool {
	if len(capabilities) == 0 {
		return false
	}
	for _, capability := range capabilities {
		if !slices.Contains(supportedCapabilities, capability) {
			return false
		}
	}
	return true
}

func uniqueCopy(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func hashSecret(secret string) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}

func newSecret() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
