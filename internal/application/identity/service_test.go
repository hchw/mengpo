package identity

import (
	"context"
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

func (f *fakeStore) UpsertUserByEmail(_ context.Context, user auth.User) (auth.User, error) {
	for _, existing := range f.users {
		if existing.Email == user.Email {
			return existing, nil
		}
	}
	f.users[user.ID] = user
	return user, nil
}

func (f *fakeStore) GetUser(_ context.Context, userID string) (auth.User, error) {
	user, ok := f.users[userID]
	if !ok {
		return auth.User{}, tenants.ErrMembershipNotFound
	}
	return user, nil
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

func TestExchangeIssuesSessionAndAuthenticates(t *testing.T) {
	store := newFakeStore()
	service, err := NewService(Options{Users: store, Memberships: store, Sessions: store, Verifier: InternalVerifier{}, SessionTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Exchange(context.Background(), "Owner@Example.com")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if result.Token == "" || result.User.Email != "owner@example.com" {
		t.Fatalf("unexpected exchange result: %#v", result)
	}
	session, user, err := service.Authenticate(context.Background(), result.Token)
	if err != nil || user.ID != result.User.ID || session.UserID != result.User.ID {
		t.Fatalf("Authenticate() = %#v %#v err=%v", session, user, err)
	}
	if _, _, err := service.Authenticate(context.Background(), "not-a-token"); err == nil {
		t.Fatal("unauthenticated token must be rejected")
	}
}

func TestExchangeRequiresVerifierAndValidAssertion(t *testing.T) {
	store := newFakeStore()
	noVerifier, err := NewService(Options{Users: store, Memberships: store, Sessions: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noVerifier.Exchange(context.Background(), "owner@example.com"); err == nil {
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
