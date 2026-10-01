package assembly

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/registry"
)

// DevSeedEmail is the development account the console signs in with. The
// internal SSO verifier trusts the typed email as a verified subject, so this
// address maps to the seeded user.
const DevSeedEmail = "dev@mengpo.local"

// DevSeed provisions a demo tenant and an active membership so the console is
// usable end to end in development. It is idempotent and must never run in
// production.
func DevSeed(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	if db == nil {
		return errors.New("database is required for dev seed")
	}
	identity := postgres.NewIdentityRepository(db)
	userID, err := newSeedID()
	if err != nil {
		return err
	}
	user, err := identity.UpsertUserByEmail(ctx, auth.User{
		ID:          userID,
		Email:       DevSeedEmail,
		DisplayName: "Dev User",
		Status:      auth.UserActive,
	})
	if err != nil {
		return fmt.Errorf("seed user: %w", err)
	}
	tenantID, err := ensureActiveTenant(ctx, db, registry.NewStore(db))
	if err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}
	membershipID, err := newSeedID()
	if err != nil {
		return err
	}
	if err := identity.CreateMembership(ctx, auth.TenantMembership{
		ID:       membershipID,
		UserID:   user.ID,
		TenantID: tenantID,
		Status:   auth.MembershipActive,
	}); err != nil {
		return fmt.Errorf("seed membership: %w", err)
	}
	if logger != nil {
		logger.Info("dev seed ready", "email", DevSeedEmail, "tenant_id", tenantID)
	}
	return nil
}

// ensureActiveTenant reuses the first active tenant or provisions a demo one.
func ensureActiveTenant(ctx context.Context, db *sql.DB, store *registry.Store) (string, error) {
	var tenantID string
	err := db.QueryRowContext(ctx, `
		SELECT t.id::text
		FROM public.tenants AS t
		JOIN public.tenant_schema_registry AS r ON r.tenant_id = t.id
		WHERE r.state = 'enabled' AND t.status = 'active'
		ORDER BY t.created_at
		LIMIT 1`).Scan(&tenantID)
	if err == nil {
		return tenantID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	tenant, err := store.ProvisionTenant(ctx, "Demo")
	if err != nil {
		return "", err
	}
	return tenant.ID, nil
}

func newSeedID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
