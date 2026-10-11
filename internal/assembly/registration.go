package assembly

import (
	"context"
	"fmt"
	"strings"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
)

// registrationProvisioner gives a freshly registered user a personal tenant so
// the console is usable immediately after sign-up, instead of dead-ending on a
// missing membership.
type registrationProvisioner struct {
	store    *registry.Store
	identity *postgres.IdentityRepository
}

// NewRegistrationProvisioner wires personal-tenant provisioning for sign-up.
func NewRegistrationProvisioner(store *registry.Store, identity *postgres.IdentityRepository) *registrationProvisioner {
	return &registrationProvisioner{store: store, identity: identity}
}

func (p *registrationProvisioner) ProvisionPersonalTenant(ctx context.Context, user auth.User) error {
	if p == nil || p.store == nil || p.identity == nil {
		return nil
	}
	tenant, err := p.store.ProvisionTenant(ctx, personalTenantName(user))
	if err != nil {
		return fmt.Errorf("provision personal tenant: %w", err)
	}
	membershipID, err := newSeedID()
	if err != nil {
		return err
	}
	if err := p.identity.CreateMembershipWithRole(ctx, auth.TenantMembership{
		ID:       membershipID,
		UserID:   user.ID,
		TenantID: tenant.ID,
		Status:   auth.MembershipActive,
	}, "owner"); err != nil {
		return fmt.Errorf("grant personal tenant ownership: %w", err)
	}
	return nil
}

// personalTenantName derives a readable tenant name from the account.
func personalTenantName(user auth.User) string {
	base := strings.TrimSpace(user.DisplayName)
	if base == "" || base == user.Email {
		base, _, _ = strings.Cut(user.Email, "@")
	}
	if base == "" {
		base = "personal"
	}
	return base + " workspace"
}
