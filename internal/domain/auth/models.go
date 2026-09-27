package auth

import "time"

// User is a local account authenticated by the Memory Service.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Status       UserStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type UserStatus string

const (
	UserActive   UserStatus = "active"
	UserDisabled UserStatus = "disabled"
)

// Tenant is the platform-level identity for a tenant-owned schema.
type Tenant struct {
	ID        string
	Name      string
	Schema    string
	Status    TenantStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

type TenantStatus string

const (
	TenantProvisioning TenantStatus = "provisioning"
	TenantActive       TenantStatus = "active"
	TenantSuspended    TenantStatus = "suspended"
	TenantDeleting     TenantStatus = "deleting"
)

// TenantMembership grants a user access to a tenant through one or more roles.
type TenantMembership struct {
	ID        string
	UserID    string
	TenantID  string
	RoleIDs   []string
	Status    MembershipStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

type MembershipStatus string

const (
	MembershipInvited   MembershipStatus = "invited"
	MembershipActive    MembershipStatus = "active"
	MembershipSuspended MembershipStatus = "suspended"
	MembershipRemoved   MembershipStatus = "removed"
)

// IsActiveFor verifies that this membership is active for the exact user and tenant.
func (m TenantMembership) IsActiveFor(userID, tenantID string) bool {
	return userID != "" && tenantID != "" &&
		m.UserID == userID && m.TenantID == tenantID &&
		m.Status == MembershipActive
}

// Role is tenant-scoped unless TenantID is empty, in which case it is a platform role.
type Role struct {
	ID          string
	TenantID    string
	Name        string
	Permissions []Permission
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Permission is a named capability; authorization policy maps these to operations.
type Permission struct {
	Code        string
	Description string
}

// Agent is a tenant-bound machine principal. Agent identity is not a memory scope.
type Agent struct {
	ID                string
	TenantID          string
	Name              string
	AllowedUserIDs    []string
	AllowedSessionIDs []string
	AllowedScopes     []string
	Capabilities      []string
	Status            AgentStatus
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type AgentStatus string

const (
	AgentActive   AgentStatus = "active"
	AgentDisabled AgentStatus = "disabled"
)

// AgentCredential stores only a verifier/hash, never the issued secret itself.
type AgentCredential struct {
	ID         string
	AgentID    string
	TenantID   string
	SecretHash []byte
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// AuthSession is a local user session bound to one tenant context.
// TokenHash contains a one-way digest of the opaque session token.
type AuthSession struct {
	ID             string
	UserID         string
	TenantID       string
	MembershipID   string
	TokenHash      []byte
	AuthzVersion   uint64
	CreatedAt      time.Time
	ExpiresAt      time.Time
	LastSeenAt     time.Time
	RevokedAt      *time.Time
	RevocationNote string
}
