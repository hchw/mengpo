package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/ports"
)

// IdentityRepository persists platform identity: users, tenant memberships and
// local auth sessions. It never touches tenant memory schemas.
type IdentityRepository struct{ db *sql.DB }

func NewIdentityRepository(db *sql.DB) *IdentityRepository { return &IdentityRepository{db: db} }

// UpsertUserByEmail finds a user by email or creates it. It is the entry point
// for SSO exchanges, where the provider asserts a verified email.
func (r *IdentityRepository) UpsertUserByEmail(ctx context.Context, user auth.User) (auth.User, error) {
	if user.Email == "" || user.ID == "" {
		return auth.User{}, errors.New("user id and email are required")
	}
	var stored auth.User
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO public.users (id, email, display_name, password_hash, status)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (email) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			updated_at = now()
		RETURNING id::text, email, display_name, password_hash, status, created_at, updated_at`,
		user.ID, user.Email, user.DisplayName, user.PasswordHash, userStatus(user.Status)).
		Scan(&stored.ID, &stored.Email, &stored.DisplayName, &stored.PasswordHash, &stored.Status, &stored.CreatedAt, &stored.UpdatedAt)
	if err != nil {
		return auth.User{}, fmt.Errorf("upsert user: %w", err)
	}
	return stored, nil
}

// CreateMembership inserts an active tenant membership for a user. It is
// idempotent per (user, tenant) and is used by development seeding and admin
// provisioning.
func (r *IdentityRepository) CreateMembership(ctx context.Context, membership auth.TenantMembership) error {
	if membership.ID == "" || membership.UserID == "" || membership.TenantID == "" {
		return errors.New("membership id, user and tenant are required")
	}
	status := membership.Status
	if status == "" {
		status = auth.MembershipActive
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.tenant_memberships (id, user_id, tenant_id, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
		ON CONFLICT (user_id, tenant_id) DO NOTHING`,
		membership.ID, membership.UserID, membership.TenantID, string(status))
	if err != nil {
		return fmt.Errorf("create membership: %w", err)
	}
	return nil
}

// GetUser loads one user by id.
func (r *IdentityRepository) GetUser(ctx context.Context, userID string) (auth.User, error) {
	var stored auth.User
	err := r.db.QueryRowContext(ctx, `SELECT id::text, email, display_name, password_hash, status, created_at, updated_at FROM public.users WHERE id = $1::uuid`, userID).
		Scan(&stored.ID, &stored.Email, &stored.DisplayName, &stored.PasswordHash, &stored.Status, &stored.CreatedAt, &stored.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, tenants.ErrMembershipNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("get user: %w", err)
	}
	return stored, nil
}

// ListMembershipsForUser returns active and inactive memberships with their
// roles and permissions, satisfying tenants.MembershipRepository.
func (r *IdentityRepository) ListMembershipsForUser(ctx context.Context, userID string) ([]tenants.MembershipRecord, error) {
	if userID == "" {
		return nil, tenants.ErrMembershipNotFound
	}
	rows, err := r.db.QueryContext(ctx, membershipQuery+` WHERE m.user_id = $1::uuid ORDER BY t.id, r.id, p.code`, userID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	return scanMemberships(rows)
}

// GetMembership returns one membership for a user and tenant.
func (r *IdentityRepository) GetMembership(ctx context.Context, userID, tenantID string) (tenants.MembershipRecord, error) {
	if userID == "" || tenantID == "" {
		return tenants.MembershipRecord{}, tenants.ErrMembershipNotFound
	}
	rows, err := r.db.QueryContext(ctx, membershipQuery+` WHERE m.user_id = $1::uuid AND m.tenant_id = $2::uuid ORDER BY r.id, p.code`, userID, tenantID)
	if err != nil {
		return tenants.MembershipRecord{}, fmt.Errorf("get membership: %w", err)
	}
	defer rows.Close()
	records, err := scanMemberships(rows)
	if err != nil {
		return tenants.MembershipRecord{}, err
	}
	if len(records) == 0 {
		return tenants.MembershipRecord{}, tenants.ErrMembershipNotFound
	}
	return records[0], nil
}

const membershipQuery = `
	SELECT u.id::text, u.email, u.display_name, u.password_hash, u.status, u.created_at, u.updated_at,
	       t.id::text, t.name, COALESCE(sr.schema_name, ''), t.status, t.created_at, t.updated_at,
	       m.id::text, m.user_id::text, m.tenant_id::text, m.status, m.created_at, m.updated_at,
	       COALESCE(r.id::text, ''), COALESCE(r.tenant_id::text, ''), COALESCE(r.name, ''),
	       COALESCE(p.code, ''), COALESCE(p.description, '')
	FROM public.tenant_memberships m
	JOIN public.users u ON u.id = m.user_id
	JOIN public.tenants t ON t.id = m.tenant_id
	LEFT JOIN public.tenant_schema_registry sr ON sr.tenant_id = t.id
	LEFT JOIN public.membership_roles mr ON mr.membership_id = m.id
	LEFT JOIN public.roles r ON r.id = mr.role_id
	LEFT JOIN public.role_permissions rp ON rp.role_id = r.id
	LEFT JOIN public.permissions p ON p.code = rp.permission_code`

func scanMemberships(rows *sql.Rows) ([]tenants.MembershipRecord, error) {
	order := make([]string, 0)
	byID := map[string]*tenants.MembershipRecord{}
	roles := map[string]map[string]*auth.Role{}
	for rows.Next() {
		var record tenants.MembershipRecord
		var tenantSchema string
		var roleID, roleTenantID, roleName, permissionCode, permissionDescription string
		if err := rows.Scan(
			&record.User.ID, &record.User.Email, &record.User.DisplayName, &record.User.PasswordHash, &record.User.Status, &record.User.CreatedAt, &record.User.UpdatedAt,
			&record.Tenant.ID, &record.Tenant.Name, &tenantSchema, &record.Tenant.Status, &record.Tenant.CreatedAt, &record.Tenant.UpdatedAt,
			&record.Membership.ID, &record.Membership.UserID, &record.Membership.TenantID, &record.Membership.Status, &record.Membership.CreatedAt, &record.Membership.UpdatedAt,
			&roleID, &roleTenantID, &roleName, &permissionCode, &permissionDescription,
		); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		record.Tenant.Schema = tenantSchema
		if _, ok := byID[record.Membership.ID]; !ok {
			copyRecord := record
			byID[record.Membership.ID] = &copyRecord
			roles[record.Membership.ID] = map[string]*auth.Role{}
			order = append(order, record.Membership.ID)
		}
		if roleID == "" {
			continue
		}
		role, ok := roles[record.Membership.ID][roleID]
		if !ok {
			role = &auth.Role{ID: roleID, TenantID: roleTenantID, Name: roleName}
			roles[record.Membership.ID][roleID] = role
		}
		if permissionCode != "" {
			role.Permissions = append(role.Permissions, auth.Permission{Code: permissionCode, Description: permissionDescription})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memberships: %w", err)
	}
	records := make([]tenants.MembershipRecord, 0, len(order))
	for _, id := range order {
		record := byID[id]
		roleMap := roles[id]
		roleIDs := make([]string, 0, len(roleMap))
		for _, role := range roleMap {
			record.Roles = append(record.Roles, *role)
			roleIDs = append(roleIDs, role.ID)
		}
		record.Membership.RoleIDs = roleIDs
		records = append(records, *record)
	}
	return records, nil
}

// ListMembersForTenant returns the members of one tenant with their primary
// role name for the console.
func (r *IdentityRepository) ListMembersForTenant(ctx context.Context, tenantID string) ([]ports.MemberRecord, error) {
	if tenantID == "" {
		return nil, tenants.ErrMembershipNotFound
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT u.id::text, u.email, COALESCE(r.name, ''), m.status
		FROM public.tenant_memberships m
		JOIN public.users u ON u.id = m.user_id
		LEFT JOIN public.membership_roles mr ON mr.membership_id = m.id
		LEFT JOIN public.roles r ON r.id = mr.role_id
		WHERE m.tenant_id = $1::uuid
		ORDER BY u.email, r.name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()
	byUser := map[string]*ports.MemberRecord{}
	order := make([]string, 0)
	for rows.Next() {
		var userID, email, role, status string
		if err := rows.Scan(&userID, &email, &role, &status); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		record, ok := byUser[userID]
		if !ok {
			record = &ports.MemberRecord{UserID: userID, Email: email, Role: role, Status: status}
			byUser[userID] = record
			order = append(order, userID)
			continue
		}
		if record.Role == "" && role != "" {
			record.Role = role
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate members: %w", err)
	}
	members := make([]ports.MemberRecord, 0, len(order))
	for _, id := range order {
		members = append(members, *byUser[id])
	}
	return members, nil
}
func (r *IdentityRepository) CreateSession(ctx context.Context, session auth.AuthSession) error {
	if session.ID == "" || session.UserID == "" || len(session.TokenHash) == 0 || session.ExpiresAt.IsZero() {
		return errors.New("invalid auth session")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO public.auth_sessions (id, user_id, tenant_id, membership_id, token_hash, authz_version, expires_at)
		VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7)`,
		session.ID, session.UserID, session.TenantID, session.MembershipID, session.TokenHash, session.AuthzVersion, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create auth session: %w", err)
	}
	return nil
}

// ResolveSession loads an active session by token hash and records last_seen_at.
func (r *IdentityRepository) ResolveSession(ctx context.Context, tokenHash []byte) (auth.AuthSession, error) {
	var session auth.AuthSession
	var tenantID, membershipID sql.NullString
	var revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		UPDATE public.auth_sessions
		SET last_seen_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING id::text, user_id::text, tenant_id::text, membership_id::text, token_hash, authz_version, created_at, expires_at, last_seen_at, revoked_at, revocation_note`,
		tokenHash).Scan(&session.ID, &session.UserID, &tenantID, &membershipID, &session.TokenHash, &session.AuthzVersion, &session.CreatedAt, &session.ExpiresAt, &session.LastSeenAt, &revokedAt, &session.RevocationNote)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.AuthSession{}, tenants.ErrMembershipNotFound
	}
	if err != nil {
		return auth.AuthSession{}, fmt.Errorf("resolve session: %w", err)
	}
	session.TenantID = tenantID.String
	session.MembershipID = membershipID.String
	if revokedAt.Valid {
		session.RevokedAt = &revokedAt.Time
	}
	return session, nil
}

// SelectTenantOnSession binds an existing session to a chosen tenant context.
func (r *IdentityRepository) SelectTenantOnSession(ctx context.Context, sessionID, tenantID, membershipID string) error {
	result, err := r.db.ExecContext(ctx, `UPDATE public.auth_sessions SET tenant_id = $2::uuid, membership_id = $3::uuid WHERE id = $1::uuid AND revoked_at IS NULL`, sessionID, tenantID, membershipID)
	if err != nil {
		return fmt.Errorf("select tenant on session: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return tenants.ErrMembershipNotFound
	}
	return nil
}

// RevokeSession marks a session as revoked.
func (r *IdentityRepository) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE public.auth_sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, sessionID)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func userStatus(status auth.UserStatus) auth.UserStatus {
	if status == "" {
		return auth.UserActive
	}
	return status
}
