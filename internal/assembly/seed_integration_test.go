package assembly

import (
	"context"
	"os"
	"testing"

	"github.com/hchw/mengpo/internal/adapters/postgres"
	"github.com/hchw/mengpo/internal/domain/auth"
)

// TestDevSeedIsIdempotent provisions the demo tenant and membership twice and
// verifies the seeded account can reach exactly one active tenant.
func TestDevSeedIsIdempotent(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	db, err := OpenDB(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := DevSeed(ctx, db, nil); err != nil {
			t.Fatalf("dev seed run %d: %v", i+1, err)
		}
	}
	repo := postgres.NewIdentityRepository(db)
	userID, err := newSeedID()
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.UpsertUserByEmail(ctx, auth.User{ID: userID, Email: DevSeedEmail, DisplayName: "Dev User", Status: auth.UserActive})
	if err != nil {
		t.Fatal(err)
	}
	memberships, err := repo.ListMembershipsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 {
		t.Fatalf("expected exactly one seeded membership, got %d", len(memberships))
	}
	if memberships[0].Tenant.Status != "active" || memberships[0].Tenant.Schema == "" {
		t.Fatalf("seeded tenant is not routable: %+v", memberships[0].Tenant)
	}
}
