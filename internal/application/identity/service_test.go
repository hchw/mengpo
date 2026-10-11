package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
)

type fakeStore struct {
	users       map[string]auth.User
	sessions    map[string]auth.AuthSession
	memberships []tenants.MembershipRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]auth.User{}, sessions: map[string]auth.AuthSession{}}
}

func (f *fakeStore) CreateUser(_ context.Context, user auth.User) (auth.User, error) {
	for _, existing := range f.users {
		if existing.Email == user.Email {
			return auth.User{}, auth.ErrEmailExists
		}
	}
	f.users[user.ID] = user
	return user, nil
}

func (f *fakeStore) GetUser(_ context.Context, userID string) (auth.User, error) {
	user, ok := f.users[userID]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return user, nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (auth.User, error) {
	for _, user := range f.users {
		if user.Email == email {
			return user, nil
		}
	}
	return auth.User{}, auth.ErrUserNotFound
}

func (f *fakeStore) ListMembershipsForUser(_ context.Context, userID string) ([]tenants.MembershipRecord, error) {
	var out []tenants.MembershipRecord
	for _, record := range f.memberships {
		if record.User.ID == userID {
			out = append(out, record)
		}
	}
	return out, nil
}

func (f *fakeStore) GetMembership(_ context.Context, userID, tenantID string) (tenants.MembershipRecord, error) {
	for _, record := range f.memberships {
		if record.User.ID == userID && record.Tenant.ID == tenantID {
			return record, nil
		}
	}
	return tenants.MembershipRecord{}, tenants.ErrMembershipNotFound
}

func (f *fakeStore) CreateSession(_ context.Context, session auth.AuthSession) error {
	f.sessions[string(session.TokenHash)] = session
	return nil
}

func (f *fakeStore) ResolveSession(_ context.Context, tokenHash []byte) (auth.AuthSession, error) {
	session, ok := f.sessions[string(tokenHash)]
	if !ok {
		return auth.AuthSession{}, tenants.ErrMembershipNotFound
	}
	return session, nil
}

func (f *fakeStore) SelectTenantOnSession(context.Context, string, string, string) error {
	return nil
}
func (f *fakeStore) RevokeSession(context.Context, string) error { return nil }

// fakeProvisioner records the users it provisioned a tenant for.
type fakeProvisioner struct {
	provisioned []string
}

func (p *fakeProvisioner) ProvisionPersonalTenant(_ context.Context, user auth.User) error {
	p.provisioned = append(p.provisioned, user.ID)
	return nil
}

func TestRegisterIssuesSessionAndAuthenticates(t *testing.T) {
	store := newFakeStore()
	provisioner := &fakeProvisioner{}
	service, err := NewService(Options{Users: store, Memberships: store, Sessions: store, Provisioner: provisioner, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Register(context.Background(), RegisterInput{Email: "Owner@Example.com", Password: "supersecret"})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if result.Token == "" || result.User.Email != "owner@example.com" {
		t.Fatalf("unexpected register result: %#v", result)
	}
	if len(provisioner.provisioned) != 1 || provisioner.provisioned[0] != result.User.ID {
		t.Fatalf("expected personal tenant provisioning for the new user, got %#v", provisioner.provisioned)
	}
	session, user, err := service.Authenticate(context.Background(), result.Token)
	if err != nil || user.ID != result.User.ID || session.UserID != result.User.ID {
		t.Fatalf("Authenticate() = %#v %#v err=%v", session, user, err)
	}
	if _, _, err := service.Authenticate(context.Background(), "not-a-token"); err == nil {
		t.Fatal("unauthenticated token must be rejected")
	}
}

func TestRegisterRejectsInvalidInputAndDuplicateEmail(t *testing.T) {
	store := newFakeStore()
	service, err := NewService(Options{Users: store, Memberships: store, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), RegisterInput{Email: "not-an-email", Password: "supersecret"}); !errors.Is(err, ErrInvalidEmail) {
		t.Fatalf("invalid email error = %v", err)
	}
	if _, err := service.Register(context.Background(), RegisterInput{Email: "a@b.com", Password: "short"}); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password error = %v", err)
	}
	if _, err := service.Register(context.Background(), RegisterInput{Email: "a@b.com", Password: "supersecret"}); err != nil {
		t.Fatalf("first register error = %v", err)
	}
	if _, err := service.Register(context.Background(), RegisterInput{Email: "A@B.com", Password: "supersecret"}); !errors.Is(err, auth.ErrEmailExists) {
		t.Fatalf("duplicate email error = %v", err)
	}
}

func TestExchangeUpsertsUserAndIssuesSession(t *testing.T) {
	store := newFakeStore()
	service, err := NewService(Options{Users: store, Memberships: store, Sessions: store, Verifier: InternalVerifier{}, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Exchange(context.Background(), "Pi@Example.com")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if first.User.Email != "pi@example.com" {
		t.Fatalf("unexpected exchange user: %#v", first.User)
	}
	second, err := service.Exchange(context.Background(), "pi@example.com")
	if err != nil {
		t.Fatalf("second Exchange() error = %v", err)
	}
	if second.User.ID != first.User.ID {
		t.Fatalf("exchange must reuse the existing user, got %s vs %s", second.User.ID, first.User.ID)
	}
}

func TestExchangeRequiresVerifierAndValidAssertion(t *testing.T) {
	store := newFakeStore()
	noVerifier, err := NewService(Options{Users: store, Memberships: store, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noVerifier.Exchange(context.Background(), "pi@example.com"); err == nil {
		t.Fatal("exchange without a verifier must fail")
	}
	withVerifier, err := NewService(Options{Users: store, Memberships: store, Sessions: store, Verifier: InternalVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withVerifier.Exchange(context.Background(), "not-an-email"); err == nil {
		t.Fatal("invalid assertion must be rejected")
	}
}

func TestLoginVerifiesPassword(t *testing.T) {
	store := newFakeStore()
	service, err := NewService(Options{Users: store, Memberships: store, Sessions: store, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register(context.Background(), RegisterInput{Email: "owner@example.com", Password: "supersecret"}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Login(context.Background(), "owner@example.com", "supersecret")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.Token == "" {
		t.Fatal("login did not issue a token")
	}
	if _, err := service.Login(context.Background(), "owner@example.com", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, err := service.Login(context.Background(), "missing@example.com", "supersecret"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user error = %v", err)
	}
}
