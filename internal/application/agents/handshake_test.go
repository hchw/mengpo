package agents

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
)

type memoryRepository struct {
	agents      map[string]auth.Agent
	credentials map[string]auth.AgentCredential
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{agents: make(map[string]auth.Agent), credentials: make(map[string]auth.AgentCredential)}
}

func (r *memoryRepository) CreateAgent(_ context.Context, agent auth.Agent, credential auth.AgentCredential) error {
	r.agents[agent.ID] = agent
	r.credentials[agent.ID] = credential
	return nil
}

func (r *memoryRepository) RotateCredential(_ context.Context, agentID string, credential auth.AgentCredential) error {
	agent, ok := r.agents[agentID]
	if !ok || agent.TenantID != credential.TenantID {
		return ErrInvalidAgent
	}
	r.credentials[agentID] = credential
	return nil
}

func (r *memoryRepository) FindByCredentialHash(_ context.Context, secretHash []byte) (auth.Agent, auth.AgentCredential, error) {
	for agentID, credential := range r.credentials {
		if bytes.Equal(secretHash, credential.SecretHash) {
			return r.agents[agentID], credential, nil
		}
	}
	return auth.Agent{}, auth.AgentCredential{}, ErrInvalidAgent
}

func tenantAdminContext(tenantID string) tenants.ActiveTenantContext {
	return tenants.ActiveTenantContext{
		UserID:      "admin-1",
		Tenant:      auth.Tenant{ID: tenantID, Status: auth.TenantActive},
		Permissions: []string{PermissionManageAgent},
	}
}

func TestRegisterBindsAgentToActiveTenantAndStoresOnlyHash(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)

	agent, secret, err := service.Register(context.Background(), tenantAdminContext("tenant-a"), RegisterRequest{
		Name:              "observer",
		AllowedUserIDs:    []string{"user-1"},
		AllowedSessionIDs: []string{"session-1"},
		AllowedScopes:     []string{ScopeUserGlobal, ScopeSession},
		Capabilities:      []string{"observe", "project"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if agent.TenantID != "tenant-a" || secret == "" {
		t.Fatalf("registered agent = %#v, secret empty = %t", agent, secret == "")
	}
	stored := repository.credentials[agent.ID]
	if bytes.Equal(stored.SecretHash, []byte(secret)) || bytes.Equal(stored.SecretHash, hashSecret(secret)) == false {
		t.Fatal("repository must store only the one-way credential verifier")
	}
}

func TestRegisterRequiresTenantAdminAndRejectsNonMemoryScope(t *testing.T) {
	service := NewService(newMemoryRepository())
	request := RegisterRequest{
		Name: "agent", AllowedUserIDs: []string{"user-1"},
		AllowedScopes: []string{"project"}, Capabilities: []string{"observe"},
	}
	if _, _, err := service.Register(context.Background(), tenantAdminContext("tenant-a"), request); !errors.Is(err, ErrInvalidAgent) {
		t.Fatalf("invalid scope error = %v, want ErrInvalidAgent", err)
	}
	request.AllowedScopes = []string{ScopeSession}
	contextWithoutPermission := tenantAdminContext("tenant-a")
	contextWithoutPermission.Permissions = nil
	if _, _, err := service.Register(context.Background(), contextWithoutPermission, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing permission error = %v, want ErrForbidden", err)
	}
}

func TestRotateCredentialInvalidatesOldCredential(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	agent, oldSecret, err := service.Register(context.Background(), tenantAdminContext("tenant-a"), RegisterRequest{
		Name: "agent", AllowedUserIDs: []string{"user-1"},
		AllowedScopes: []string{ScopeSession}, Capabilities: []string{"observe"},
	})
	if err != nil {
		t.Fatal(err)
	}
	newSecretValue, err := service.RotateCredential(context.Background(), tenantAdminContext("tenant-a"), agent.ID)
	if err != nil {
		t.Fatalf("RotateCredential() error = %v", err)
	}
	if _, err := service.Handshake(context.Background(), HandshakeRequest{
		Credential: oldSecret, ProtocolVersion: ProtocolV1, UserID: "user-1", ScopeType: ScopeSession, SessionID: "session-1",
	}); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("old credential handshake error = %v, want ErrInvalidHandshake", err)
	}
	if newSecretValue == oldSecret {
		t.Fatal("rotation must issue a new credential")
	}
}

func TestHandshakeNegotiatesCapabilitiesAndEnforcesScope(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	agent, secret, err := service.Register(context.Background(), tenantAdminContext("tenant-a"), RegisterRequest{
		Name:              "observer",
		AllowedUserIDs:    []string{"user-1"},
		AllowedSessionIDs: []string{"session-1"},
		AllowedScopes:     []string{ScopeUserGlobal, ScopeSession},
		Capabilities:      []string{"observe", "feedback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.Handshake(context.Background(), HandshakeRequest{
		Credential: secret, ProtocolVersion: ProtocolV1, UserID: "user-1",
		SessionID: "session-1", ScopeType: ScopeSession,
		RequestedCapabilities: []string{"observe"},
	})
	if err != nil {
		t.Fatalf("Handshake() error = %v", err)
	}
	if response.AgentID != agent.ID || response.TenantID != "tenant-a" || len(response.AllowedEventTypes) == 0 {
		t.Fatalf("handshake response = %#v", response)
	}
	if _, err := service.Handshake(context.Background(), HandshakeRequest{
		Credential: secret, ProtocolVersion: ProtocolV1, UserID: "other-user",
		SessionID: "session-1", ScopeType: ScopeSession,
	}); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("unauthorized user error = %v, want ErrInvalidHandshake", err)
	}
	if _, err := service.Handshake(context.Background(), HandshakeRequest{
		Credential: secret, ProtocolVersion: ProtocolV1, UserID: "user-1",
		SessionID: "other-session", ScopeType: ScopeSession,
	}); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("unauthorized session error = %v, want ErrInvalidHandshake", err)
	}
}

func TestHandshakeRejectsExpiredCredentialAndUnsupportedProtocol(t *testing.T) {
	repository := newMemoryRepository()
	service := NewService(repository)
	agent, secret, err := service.Register(context.Background(), tenantAdminContext("tenant-a"), RegisterRequest{
		Name: "agent", AllowedUserIDs: []string{"user-1"},
		AllowedScopes: []string{ScopeUserGlobal}, Capabilities: []string{"project"},
	})
	if err != nil {
		t.Fatal(err)
	}
	credential := repository.credentials[agent.ID]
	credential.ExpiresAt = time.Now().Add(-time.Minute)
	repository.credentials[agent.ID] = credential
	if _, err := service.Handshake(context.Background(), HandshakeRequest{
		Credential: secret, ProtocolVersion: ProtocolV1, UserID: "user-1", ScopeType: ScopeUserGlobal,
	}); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatalf("expired credential error = %v, want ErrInvalidHandshake", err)
	}
	if _, err := service.Handshake(context.Background(), HandshakeRequest{ProtocolVersion: "unknown"}); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("unsupported protocol error = %v, want ErrUnsupportedProtocol", err)
	}
}
