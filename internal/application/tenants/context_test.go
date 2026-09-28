package tenants

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hchw/mengpo/internal/domain/auth"
)

type memoryMembershipRepository struct {
	records []MembershipRecord
}

func (r memoryMembershipRepository) ListMembershipsForUser(_ context.Context, userID string) ([]MembershipRecord, error) {
	var matches []MembershipRecord
	for _, record := range r.records {
		if record.User.ID == userID {
			matches = append(matches, record)
		}
	}
	return matches, nil
}

func (r memoryMembershipRepository) GetMembership(_ context.Context, userID, tenantID string) (MembershipRecord, error) {
	for _, record := range r.records {
		if record.User.ID == userID && record.Tenant.ID == tenantID {
			return record, nil
		}
	}
	return MembershipRecord{}, ErrMembershipNotFound
}

func activeRecord(userID, tenantID, roleID, permission string) MembershipRecord {
	return MembershipRecord{
		User: auth.User{ID: userID, Status: auth.UserActive},
		Tenant: auth.Tenant{
			ID: tenantID, Name: tenantID, Schema: "schema_" + tenantID, Status: auth.TenantActive,
		},
		Membership: auth.TenantMembership{
			ID: "membership-" + tenantID, UserID: userID, TenantID: tenantID,
			RoleIDs: []string{roleID}, Status: auth.MembershipActive,
		},
		Roles: []auth.Role{{
			ID: roleID, TenantID: tenantID, Name: "member",
			Permissions: []auth.Permission{{Code: permission}},
		}},
	}
}

func TestListTenantsOnlyReturnsActiveMemberships(t *testing.T) {
	active := activeRecord("user-1", "tenant-a", "role-a", "memory.read")
	inactive := activeRecord("user-1", "tenant-b", "role-b", "memory.read")
	inactive.Membership.Status = auth.MembershipSuspended

	service := NewService(memoryMembershipRepository{records: []MembershipRecord{active, inactive}})
	got, err := service.ListTenants(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("ListTenants() error = %v", err)
	}
	if len(got) != 1 || got[0].Tenant.ID != "tenant-a" {
		t.Fatalf("ListTenants() = %#v, want only tenant-a", got)
	}
}

func TestSelectTenantResolvesPermissionsAndSwitchesTenantContext(t *testing.T) {
	recordA := activeRecord("user-1", "tenant-a", "role-a", "memory.read")
	recordB := activeRecord("user-1", "tenant-b", "role-b", "memory.write")
	service := NewService(memoryMembershipRepository{records: []MembershipRecord{recordA, recordB}})

	ctxA, err := service.SelectTenant(context.Background(), "user-1", "tenant-a")
	if err != nil {
		t.Fatalf("SelectTenant(tenant-a) error = %v", err)
	}
	ctxB, err := service.SelectTenant(context.Background(), "user-1", "tenant-b")
	if err != nil {
		t.Fatalf("SelectTenant(tenant-b) error = %v", err)
	}
	if ctxA.Tenant.ID != "tenant-a" || !reflect.DeepEqual(ctxA.Permissions, []string{"memory.read"}) {
		t.Fatalf("tenant-a context = %#v", ctxA)
	}
	if ctxB.Tenant.ID != "tenant-b" || !reflect.DeepEqual(ctxB.Permissions, []string{"memory.write"}) {
		t.Fatalf("tenant-b context = %#v", ctxB)
	}
	if ctxA.Tenant.Schema == ctxB.Tenant.Schema {
		t.Fatal("tenant switch reused the previous tenant schema")
	}
}

func TestSelectTenantRejectsInactiveMembershipAndForeignTenant(t *testing.T) {
	record := activeRecord("user-1", "tenant-a", "role-a", "memory.read")
	service := NewService(memoryMembershipRepository{records: []MembershipRecord{record}})

	if _, err := service.SelectTenant(context.Background(), "user-1", "tenant-b"); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("foreign tenant error = %v, want ErrMembershipNotFound", err)
	}

	record.Membership.Status = auth.MembershipSuspended
	service = NewService(memoryMembershipRepository{records: []MembershipRecord{record}})
	if _, err := service.SelectTenant(context.Background(), "user-1", "tenant-a"); !errors.Is(err, ErrInactivePrincipal) {
		t.Fatalf("suspended membership error = %v, want ErrInactivePrincipal", err)
	}
}

func TestSelectTenantRejectsRoleFromAnotherTenant(t *testing.T) {
	record := activeRecord("user-1", "tenant-a", "role-a", "memory.read")
	record.Roles[0].TenantID = "tenant-b"
	service := NewService(memoryMembershipRepository{records: []MembershipRecord{record}})

	if _, err := service.SelectTenant(context.Background(), "user-1", "tenant-a"); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("foreign role error = %v, want ErrInvalidRole", err)
	}
}
