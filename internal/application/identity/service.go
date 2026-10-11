// Package identity implements the console/agent authentication flow on top of
// platform identity persistence. It issues opaque session tokens and resolves
// trusted principals; it never trusts client-declared identity.
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidAssertion   = errors.New("sso assertion is invalid")
	ErrSessionRequired    = errors.New("a verified session is required")
	ErrInvalidCredentials = errors.New("email or password is incorrect")
	ErrInvalidEmail       = errors.New("a valid email address is required")
	ErrWeakPassword       = errors.New("password must be at least 8 characters")
	ErrAccountDisabled    = errors.New("account is disabled")
)

// SSOSubject is the verified identity a provider asserts.
type SSOSubject struct {
	Email       string
	DisplayName string
}

// SSOVerifier validates an SSO assertion and returns a verified subject. It must
// never return identity fields that were not cryptographically verified. It
// remains for programmatic clients (e.g. the pi adapter) and dev/test flows;
// the console now authenticates with email and password.
type SSOVerifier interface {
	Verify(ctx context.Context, assertion string) (SSOSubject, error)
}

// minPasswordLength is the shortest password accepted at registration.
const minPasswordLength = 8

// UserStore persists users.
type UserStore interface {
	CreateUser(ctx context.Context, user auth.User) (auth.User, error)
	GetUser(ctx context.Context, userID string) (auth.User, error)
	GetUserByEmail(ctx context.Context, email string) (auth.User, error)
}

// MembershipStore resolves memberships (satisfied by tenants.MembershipRepository).
type MembershipStore interface {
	ListMembershipsForUser(ctx context.Context, userID string) ([]tenants.MembershipRecord, error)
	GetMembership(ctx context.Context, userID, tenantID string) (tenants.MembershipRecord, error)
}

// SessionStore persists hashed session tokens.
type SessionStore interface {
	CreateSession(ctx context.Context, session auth.AuthSession) error
	ResolveSession(ctx context.Context, tokenHash []byte) (auth.AuthSession, error)
	SelectTenantOnSession(ctx context.Context, sessionID, tenantID, membershipID string) error
	RevokeSession(ctx context.Context, sessionID string) error
}

// TenantProvisioner gives a freshly registered user a working tenant context so
// the console is usable immediately after sign-up.
type TenantProvisioner interface {
	ProvisionPersonalTenant(ctx context.Context, user auth.User) error
}

// Options configures the identity service.
type Options struct {
	Users       UserStore
	Memberships MembershipStore
	Sessions    SessionStore
	Provisioner TenantProvisioner
	Verifier    SSOVerifier
	SessionTTL  time.Duration
	Now         func() time.Time
	NewID       func() (string, error)
	NewToken    func() (string, error)
}

type Service struct {
	users       UserStore
	memberships MembershipStore
	sessions    SessionStore
	provisioner TenantProvisioner
	verifier    SSOVerifier
	tenants     *tenants.Service
	sessionTTL  time.Duration
	now         func() time.Time
	newID       func() (string, error)
	newToken    func() (string, error)
}

func NewService(options Options) (*Service, error) {
	if options.Users == nil || options.Memberships == nil || options.Sessions == nil {
		return nil, errors.New("identity service requires user, membership and session stores")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	newID := options.NewID
	if newID == nil {
		newID = randomID
	}
	newToken := options.NewToken
	if newToken == nil {
		newToken = randomToken
	}
	ttl := options.SessionTTL
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &Service{
		users:       options.Users,
		memberships: options.Memberships,
		sessions:    options.Sessions,
		provisioner: options.Provisioner,
		verifier:    options.Verifier,
		tenants:     tenants.NewService(options.Memberships),
		sessionTTL:  ttl,
		now:         now,
		newID:       newID,
		newToken:    newToken,
	}, nil
}

// AuthResult is returned by a successful register or login.
type AuthResult struct {
	Token   string
	User    auth.User
	Tenants []tenants.TenantOption
}

// RegisterInput is a self-service sign-up request.
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
}

// Register creates a password account, provisions a personal tenant and issues
// a session. The raw token is returned once; only its SHA-256 hash is stored.
func (s *Service) Register(ctx context.Context, input RegisterInput) (AuthResult, error) {
	email := normalizeEmail(input.Email)
	if !validEmail(email) {
		return AuthResult{}, ErrInvalidEmail
	}
	if len(input.Password) < minPasswordLength {
		return AuthResult{}, ErrWeakPassword
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = email
	}
	hash, err := HashPassword(input.Password)
	if err != nil {
		return AuthResult{}, err
	}
	userID, err := s.newID()
	if err != nil {
		return AuthResult{}, err
	}
	user, err := s.users.CreateUser(ctx, auth.User{
		ID:           userID,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		Status:       auth.UserActive,
	})
	if err != nil {
		return AuthResult{}, err
	}
	if s.provisioner != nil {
		if err := s.provisioner.ProvisionPersonalTenant(ctx, user); err != nil {
			return AuthResult{}, err
		}
	}
	return s.issueSession(ctx, user)
}

// Login verifies a password and issues a session.
func (s *Service) Login(ctx context.Context, email, password string) (AuthResult, error) {
	user, err := s.users.GetUserByEmail(ctx, normalizeEmail(email))
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return AuthResult{}, ErrInvalidCredentials
		}
		return AuthResult{}, err
	}
	if user.Status != auth.UserActive {
		return AuthResult{}, ErrAccountDisabled
	}
	if user.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return AuthResult{}, ErrInvalidCredentials
	}
	return s.issueSession(ctx, user)
}

// Exchange verifies an SSO assertion, upserts the user, and issues a session
// token. It stays for programmatic clients that cannot present a password.
func (s *Service) Exchange(ctx context.Context, assertion string) (AuthResult, error) {
	if s.verifier == nil {
		return AuthResult{}, errors.New("no SSO verifier is configured")
	}
	subject, err := s.verifier.Verify(ctx, assertion)
	if err != nil {
		return AuthResult{}, err
	}
	if strings.TrimSpace(subject.Email) == "" {
		return AuthResult{}, ErrInvalidAssertion
	}
	userID, err := s.newID()
	if err != nil {
		return AuthResult{}, err
	}
	user, err := s.users.CreateUser(ctx, auth.User{
		ID:          userID,
		Email:       normalizeEmail(subject.Email),
		DisplayName: subject.DisplayName,
		Status:      auth.UserActive,
	})
	if errors.Is(err, auth.ErrEmailExists) {
		user, err = s.users.GetUserByEmail(ctx, normalizeEmail(subject.Email))
	}
	if err != nil {
		return AuthResult{}, err
	}
	return s.issueSession(ctx, user)
}

// issueSession mints a token, stores its hash and lists the user's tenants.
func (s *Service) issueSession(ctx context.Context, user auth.User) (AuthResult, error) {
	token, err := s.newToken()
	if err != nil {
		return AuthResult{}, err
	}
	sessionID, err := s.newID()
	if err != nil {
		return AuthResult{}, err
	}
	hash := sha256.Sum256([]byte(token))
	now := s.now().UTC()
	if err := s.sessions.CreateSession(ctx, auth.AuthSession{
		ID:        sessionID,
		UserID:    user.ID,
		TokenHash: hash[:],
		CreatedAt: now,
		ExpiresAt: now.Add(s.sessionTTL),
	}); err != nil {
		return AuthResult{}, err
	}
	options, err := s.tenants.ListTenants(ctx, user.ID)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Token: token, User: user, Tenants: options}, nil
}

// Authenticate resolves a raw session token to a session, or ErrSessionRequired.
func (s *Service) Authenticate(ctx context.Context, token string) (auth.AuthSession, auth.User, error) {
	if strings.TrimSpace(token) == "" {
		return auth.AuthSession{}, auth.User{}, ErrSessionRequired
	}
	hash := sha256.Sum256([]byte(token))
	session, err := s.sessions.ResolveSession(ctx, hash[:])
	if err != nil {
		return auth.AuthSession{}, auth.User{}, ErrSessionRequired
	}
	user, err := s.users.GetUser(ctx, session.UserID)
	if err != nil {
		return auth.AuthSession{}, auth.User{}, ErrSessionRequired
	}
	return session, user, nil
}

// ListTenants lists the tenants a verified session's user can access.
func (s *Service) ListTenants(ctx context.Context, token string) ([]tenants.TenantOption, error) {
	session, _, err := s.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	return s.tenants.ListTenants(ctx, session.UserID)
}

// SelectTenant validates the membership and binds the session to a tenant.
func (s *Service) SelectTenant(ctx context.Context, token, tenantID string) (tenants.ActiveTenantContext, error) {
	session, _, err := s.Authenticate(ctx, token)
	if err != nil {
		return tenants.ActiveTenantContext{}, err
	}
	active, err := s.tenants.SelectTenant(ctx, session.UserID, tenantID)
	if err != nil {
		return tenants.ActiveTenantContext{}, err
	}
	if err := s.sessions.SelectTenantOnSession(ctx, session.ID, tenantID, active.MembershipID); err != nil {
		return tenants.ActiveTenantContext{}, err
	}
	return active, nil
}

// Revoke invalidates a session token.
func (s *Service) Revoke(ctx context.Context, token string) error {
	session, _, err := s.Authenticate(ctx, token)
	if err != nil {
		return err
	}
	return s.sessions.RevokeSession(ctx, session.ID)
}

// HashPassword hashes a plaintext password with bcrypt.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// InternalVerifier trusts the assertion as a verified email subject. It exists
// for development and tests only; production deployments must inject an OIDC or
// JWT verifier.
type InternalVerifier struct{}

func (InternalVerifier) Verify(_ context.Context, assertion string) (SSOSubject, error) {
	email := normalizeEmail(assertion)
	if !validEmail(email) {
		return SSOSubject{}, fmt.Errorf("%w: not an email subject", ErrInvalidAssertion)
	}
	return SSOSubject{Email: email, DisplayName: email}, nil
}

// validEmail performs a minimal shape check: a local part, an @ and a dotted
// domain with no whitespace.
func validEmail(email string) bool {
	if email == "" || strings.ContainsAny(email, " \t\n") {
		return false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		return false
	}
	domain := email[at+1:]
	dot := strings.Index(domain, ".")
	return dot > 0 && dot < len(domain)-1
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
