package tenants

import (
	"context"
	"errors"
	"sort"

	"github.com/hchw/mengpo/internal/domain/auth"
)

var (
	ErrMembershipNotFound = errors.New("tenant membership not found")
	ErrInactivePrincipal  = errors.New("user, tenant, or membership is inactive")
	ErrInvalidRole        = errors.New("membership references a role outside its tenant")
)

// MembershipRecord is a repository snapshot used to resolve a tenant context.
type MembershipRecord struct {
	User       auth.User
	Tenant     auth.Tenant
	Membership auth.TenantMembership
	Roles      []auth.Role
}

type MembershipRepository interface {
	ListMembershipsForUser(ctx context.Context, userID string) ([]MembershipRecord, error)
	GetMembership(ctx context.Context, userID, tenantID string) (MembershipRecord, error)
}

// TenantOption is a tenant visible to an authenticated user.
type TenantOption struct {
	Tenant     auth.Tenant
	Membership auth.TenantMembership
	Roles      []string
}

// ActiveTenantContext is rebuilt on every tenant selection/switch. It must not be
// cached across principals or tenants.
type ActiveTenantContext struct {
	UserID       string
	Tenant       auth.Tenant
	MembershipID string
	RoleIDs      []string
	Permissions  []string
}

type Service struct {
	repository MembershipRepository
}

func NewService(repository MembershipRepository) *Service {
	return &Service{repository: repository}
}

// ListTenants lists active memberships for an authenticated user. userID must
// come from a verified local auth session, never directly from request input.
func (s *Service) ListTenants(ctx context.Context, userID string) ([]TenantOption, error) {
	if userID == "" {
		return nil, ErrInactivePrincipal
	}
	records, err := s.repository.ListMembershipsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	options := make([]TenantOption, 0, len(records))
	for _, record := range records {
		if record.User.ID != userID || record.User.Status != auth.UserActive ||
			record.Tenant.Status != auth.TenantActive ||
			!record.Membership.IsActiveFor(userID, record.Tenant.ID) {
			continue
		}
		roleNames := make([]string, 0, len(record.Roles))
		for _, role := range record.Roles {
			if role.TenantID != "" && role.TenantID != record.Tenant.ID {
				return nil, ErrInvalidRole
			}
			roleNames = append(roleNames, role.Name)
		}
		sort.Strings(roleNames)
		options = append(options, TenantOption{
			Tenant: record.Tenant, Membership: record.Membership, Roles: roleNames,
		})
	}
	return options, nil
}

// SelectTenant resolves a fresh tenant-bound context after checking the current
// user, tenant, membership, and all assigned role scopes.
func (s *Service) SelectTenant(ctx context.Context, userID, tenantID string) (ActiveTenantContext, error) {
	if userID == "" || tenantID == "" {
		return ActiveTenantContext{}, ErrMembershipNotFound
	}
	record, err := s.repository.GetMembership(ctx, userID, tenantID)
	if err != nil {
		return ActiveTenantContext{}, err
	}
	if record.User.ID != userID || record.Tenant.ID != tenantID ||
		record.User.Status != auth.UserActive || record.Tenant.Status != auth.TenantActive ||
		!record.Membership.IsActiveFor(userID, tenantID) {
		return ActiveTenantContext{}, ErrInactivePrincipal
	}

	roleIDs := make(map[string]struct{}, len(record.Roles))
	for _, id := range record.Membership.RoleIDs {
		roleIDs[id] = struct{}{}
	}
	permissions := make(map[string]struct{})
	roles := make([]auth.Role, 0, len(record.Roles))
	for _, role := range record.Roles {
		if _, assigned := roleIDs[role.ID]; !assigned {
			continue
		}
		if role.TenantID != "" && role.TenantID != tenantID {
			return ActiveTenantContext{}, ErrInvalidRole
		}
		roles = append(roles, role)
		for _, permission := range role.Permissions {
			if permission.Code != "" {
				permissions[permission.Code] = struct{}{}
			}
		}
	}
	if len(roles) != len(roleIDs) {
		return ActiveTenantContext{}, ErrInvalidRole
	}
	permissionList := make([]string, 0, len(permissions))
	for permission := range permissions {
		permissionList = append(permissionList, permission)
	}
	sort.Strings(permissionList)

	roleIDList := append([]string(nil), record.Membership.RoleIDs...)
	sort.Strings(roleIDList)
	return ActiveTenantContext{
		UserID:       userID,
		Tenant:       record.Tenant,
		MembershipID: record.Membership.ID,
		RoleIDs:      roleIDList,
		Permissions:  permissionList,
	}, nil
}
