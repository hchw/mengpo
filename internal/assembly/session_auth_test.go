package assembly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	identityapp "github.com/hchw/mengpo/internal/application/identity"
	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
)

type fakeUserStore struct{}

func (fakeUserStore) CreateUser(context.Context, auth.User) (auth.User, error) {
	return auth.User{}, nil
}
func (fakeUserStore) GetUser(context.Context, string) (auth.User, error) {
	return auth.User{ID: "user-1"}, nil
}
func (fakeUserStore) GetUserByEmail(context.Context, string) (auth.User, error) {
	return auth.User{}, nil
}

type fakeSessionStore struct{}

func (fakeSessionStore) CreateSession(context.Context, auth.AuthSession) error { return nil }
func (fakeSessionStore) ResolveSession(context.Context, []byte) (auth.AuthSession, error) {
	return auth.AuthSession{ID: "session-1", UserID: "user-1", TenantID: "tenant-1"}, nil
}
func (fakeSessionStore) SelectTenantOnSession(context.Context, string, string, string) error {
	return nil
}
func (fakeSessionStore) RevokeSession(context.Context, string) error { return nil }

type fakeMembershipStore struct{}

func (fakeMembershipStore) ListMembershipsForUser(context.Context, string) ([]tenants.MembershipRecord, error) {
	return nil, nil
}
func (fakeMembershipStore) GetMembership(context.Context, string, string) (tenants.MembershipRecord, error) {
	return tenants.MembershipRecord{}, nil
}

// TestSessionAuthenticatorGrantsSessionAndConsolidate pins the verified-user
// capability set: the console session path (also used by mengpo-pi user mode)
// must include session and consolidate, or the new gates would reject its
// session binding and consolidation calls.
func TestSessionAuthenticatorGrantsSessionAndConsolidate(t *testing.T) {
	service, err := identityapp.NewService(identityapp.Options{
		Users:       fakeUserStore{},
		Sessions:    fakeSessionStore{},
		Memberships: fakeMembershipStore{},
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	authenticator := SessionAuthenticator{Service: service, CookieName: "memory_session"}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/session", nil)
	request.AddCookie(&http.Cookie{Name: "memory_session", Value: "token-1"})
	identity, err := authenticator.Authenticate(request)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if identity.TenantID != "tenant-1" || identity.UserID != "user-1" {
		t.Fatalf("identity=%#v", identity)
	}
	for _, capability := range []string{"observe", "project", "feedback", "session", "consolidate"} {
		if !containsString(identity.Capabilities, capability) {
			t.Fatalf("verified-user session missing capability %q: %#v", capability, identity.Capabilities)
		}
	}
}
