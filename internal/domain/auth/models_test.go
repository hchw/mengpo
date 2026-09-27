package auth

import "testing"

func TestTenantMembershipIsActiveFor(t *testing.T) {
	membership := TenantMembership{
		UserID:   "user-1",
		TenantID: "tenant-1",
		Status:   MembershipActive,
	}

	tests := []struct {
		name     string
		userID   string
		tenantID string
		want     bool
	}{
		{name: "matching active membership", userID: "user-1", tenantID: "tenant-1", want: true},
		{name: "different user", userID: "user-2", tenantID: "tenant-1", want: false},
		{name: "different tenant", userID: "user-1", tenantID: "tenant-2", want: false},
		{name: "empty user", userID: "", tenantID: "tenant-1", want: false},
		{name: "empty tenant", userID: "user-1", tenantID: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := membership.IsActiveFor(tt.userID, tt.tenantID); got != tt.want {
				t.Fatalf("IsActiveFor(%q, %q) = %t, want %t", tt.userID, tt.tenantID, got, tt.want)
			}
		})
	}
}

func TestTenantMembershipRequiresActiveStatus(t *testing.T) {
	statuses := []MembershipStatus{
		MembershipInvited,
		MembershipSuspended,
		MembershipRemoved,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			membership := TenantMembership{
				UserID:   "user-1",
				TenantID: "tenant-1",
				Status:   status,
			}
			if membership.IsActiveFor("user-1", "tenant-1") {
				t.Fatalf("membership in %q status must not authorize access", status)
			}
		})
	}
}
