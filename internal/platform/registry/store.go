package registry

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hchw/mengpo/db/migrations"
	"github.com/hchw/mengpo/internal/domain/auth"
)

var (
	ErrInvalidTenantName = errors.New("tenant name is required")
	ErrTenantNotRoutable = errors.New("tenant schema is not enabled")
)

var tenantSchemaPattern = regexp.MustCompile(`^tenant_[a-f0-9]{32}$`)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func ApplyPlatformMigrations(ctx context.Context, db *sql.DB) error {
	return migrations.ApplyPlatform(ctx, db)
}

// RegisterTenant creates only platform metadata. Tenant memory operations remain
// unavailable until the schema has been created/migrated and activated.
func (s *Store) ProvisionTenant(ctx context.Context, name string) (auth.Tenant, error) {
	tenant, err := s.RegisterTenant(ctx, name)
	if err != nil {
		return auth.Tenant{}, err
	}
	if !tenantSchemaPattern.MatchString(tenant.Schema) {
		return tenant, fmt.Errorf("refusing unsafe tenant schema name %q", tenant.Schema)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE SCHEMA `+tenant.Schema); err != nil {
		return tenant, fmt.Errorf("create tenant schema: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return tenant, fmt.Errorf("begin tenant migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, tenant.Schema+", public"); err != nil {
		return tenant, fmt.Errorf("set tenant migration search_path: %w", err)
	}
	if err := migrations.ApplyTenant(ctx, tx, migrations.TenantFS()); err != nil {
		return tenant, fmt.Errorf("apply tenant migrations: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&version); err != nil {
		return tenant, fmt.Errorf("read tenant migration version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET migration_version = $1, updated_at = now() WHERE tenant_id = $2`, version, tenant.ID); err != nil {
		return tenant, fmt.Errorf("record tenant migration version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return tenant, fmt.Errorf("commit tenant migrations: %w", err)
	}
	if err := s.ActivateTenant(ctx, tenant.ID); err != nil {
		return tenant, fmt.Errorf("activate provisioned tenant: %w", err)
	}
	tenant.Status = auth.TenantActive
	return tenant, nil
}

func (s *Store) RegisterTenant(ctx context.Context, name string) (auth.Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return auth.Tenant{}, ErrInvalidTenantName
	}
	id, err := newUUID()
	if err != nil {
		return auth.Tenant{}, err
	}
	schemaName := schemaNameForID(id)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return auth.Tenant{}, fmt.Errorf("begin tenant registration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.tenants (id, name, status)
		VALUES ($1, $2, 'provisioning')`, id, name); err != nil {
		return auth.Tenant{}, fmt.Errorf("insert tenant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO public.tenant_schema_registry (tenant_id, schema_name, state)
		VALUES ($1, $2, 'provisioning')`, id, schemaName); err != nil {
		return auth.Tenant{}, fmt.Errorf("insert tenant schema route: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return auth.Tenant{}, fmt.Errorf("commit tenant registration: %w", err)
	}
	return auth.Tenant{ID: id, Name: name, Schema: schemaName, Status: auth.TenantProvisioning}, nil
}

// ActivateTenant enables routing only after the schema physically exists.
func (s *Store) ActivateTenant(ctx context.Context, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant activation: %w", err)
	}
	defer tx.Rollback()
	var schemaName, state, tenantStatus string
	var recordedVersion int
	if err := tx.QueryRowContext(ctx, `
		SELECT r.schema_name, r.state, r.migration_version, t.status
		FROM public.tenant_schema_registry r
		JOIN public.tenants t ON t.id = r.tenant_id
		WHERE r.tenant_id = $1
		FOR UPDATE OF r, t`, tenantID).Scan(&schemaName, &state, &recordedVersion, &tenantStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTenantNotRoutable
		}
		return fmt.Errorf("load tenant route: %w", err)
	}
	if !tenantSchemaPattern.MatchString(schemaName) {
		return fmt.Errorf("refusing unsafe tenant schema name %q", schemaName)
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)`, schemaName).Scan(&exists); err != nil {
		return fmt.Errorf("check tenant schema: %w", err)
	}
	if !exists {
		return ErrTenantNotRoutable
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, schemaName+", public"); err != nil {
		return fmt.Errorf("set tenant validation search_path: %w", err)
	}
	var appliedVersion int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM tenant_migrations`).Scan(&appliedVersion); err != nil {
		return ErrTenantNotRoutable
	}
	if recordedVersion <= 0 || appliedVersion != recordedVersion {
		return ErrTenantNotRoutable
	}
	if state == "enabled" {
		if tenantStatus != string(auth.TenantActive) {
			return ErrTenantNotRoutable
		}
		return tx.Commit()
	}
	if state != "provisioning" || tenantStatus != string(auth.TenantProvisioning) {
		return ErrTenantNotRoutable
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE public.tenant_schema_registry
		SET state = 'enabled', updated_at = now()
		WHERE tenant_id = $1`, tenantID); err != nil {
		return fmt.Errorf("enable tenant schema route: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE public.tenants
		SET status = 'active', updated_at = now()
		WHERE id = $1 AND status = 'provisioning'`, tenantID)
	if err != nil {
		return fmt.Errorf("activate tenant: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("verify tenant activation: %w", err)
	}
	if rows != 1 {
		return ErrTenantNotRoutable
	}
	return tx.Commit()
}

// RollbackProvisioningTenant reverts exactly one migration only while the tenant
// has never been enabled for application traffic.
func (s *Store) RollbackProvisioningTenant(ctx context.Context, tenantID string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tenant rollback: %w", err)
	}
	defer tx.Rollback()
	var schemaName, routeState, tenantStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.schema_name, r.state, t.status
		FROM public.tenant_schema_registry r
		JOIN public.tenants t ON t.id = r.tenant_id
		WHERE r.tenant_id = $1
		FOR UPDATE OF r, t`, tenantID).Scan(&schemaName, &routeState, &tenantStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrTenantNotRoutable
		}
		return 0, fmt.Errorf("load tenant for rollback: %w", err)
	}
	if routeState != "provisioning" || tenantStatus != string(auth.TenantProvisioning) || !tenantSchemaPattern.MatchString(schemaName) {
		return 0, ErrTenantNotRoutable
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)`, schemaName).Scan(&exists); err != nil {
		return 0, fmt.Errorf("check tenant schema for rollback: %w", err)
	}
	if !exists {
		return 0, ErrTenantNotRoutable
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, schemaName+", public"); err != nil {
		return 0, fmt.Errorf("set tenant rollback search_path: %w", err)
	}
	version, err := migrations.RollbackTenant(ctx, tx, migrations.TenantFS())
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET migration_version = $1, updated_at = now() WHERE tenant_id = $2`, version, tenantID); err != nil {
		return 0, fmt.Errorf("update tenant migration version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit tenant rollback: %w", err)
	}
	return version, nil
}

func (s *Store) PauseTenant(ctx context.Context, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant pause: %w", err)
	}
	defer tx.Rollback()
	var routeState, tenantStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.state, t.status
		FROM public.tenant_schema_registry r
		JOIN public.tenants t ON t.id = r.tenant_id
		WHERE r.tenant_id = $1
		FOR UPDATE OF r, t`, tenantID).Scan(&routeState, &tenantStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTenantNotRoutable
		}
		return fmt.Errorf("load tenant for pause: %w", err)
	}
	if routeState == "suspended" && tenantStatus == string(auth.TenantSuspended) {
		return tx.Commit()
	}
	if routeState != "enabled" || tenantStatus != string(auth.TenantActive) {
		return ErrTenantNotRoutable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.tenant_schema_registry SET state = 'suspended', updated_at = now() WHERE tenant_id = $1`, tenantID); err != nil {
		return fmt.Errorf("suspend tenant route: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE public.tenants SET status = 'suspended', updated_at = now() WHERE id = $1`, tenantID); err != nil {
		return fmt.Errorf("suspend tenant: %w", err)
	}
	return tx.Commit()
}

// DeleteTenant permanently drops a provisioning or suspended tenant. Active
// tenants must first be paused so they cannot receive new requests.
func (s *Store) DeleteTenant(ctx context.Context, tenantID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant deletion: %w", err)
	}
	defer tx.Rollback()
	var schemaName, routeState, tenantStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.schema_name, r.state, t.status
		FROM public.tenant_schema_registry r
		JOIN public.tenants t ON t.id = r.tenant_id
		WHERE r.tenant_id = $1
		FOR UPDATE OF r, t`, tenantID).Scan(&schemaName, &routeState, &tenantStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load tenant for deletion: %w", err)
	}
	if routeState == "enabled" || tenantStatus == string(auth.TenantActive) || !tenantSchemaPattern.MatchString(schemaName) {
		return ErrTenantNotRoutable
	}
	if _, err := tx.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schemaName+` CASCADE`); err != nil {
		return fmt.Errorf("drop tenant schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM public.tenants WHERE id = $1`, tenantID); err != nil {
		return fmt.Errorf("delete tenant registration: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ResolveSchema(ctx context.Context, tenantID string) (string, error) {
	var schemaName string
	err := s.db.QueryRowContext(ctx, `
		SELECT r.schema_name
		FROM public.tenant_schema_registry AS r
		JOIN public.tenants AS t ON t.id = r.tenant_id
		WHERE r.tenant_id = $1 AND r.state = 'enabled' AND t.status = 'active'`, tenantID).Scan(&schemaName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTenantNotRoutable
	}
	if err != nil {
		return "", fmt.Errorf("resolve tenant schema: %w", err)
	}
	if !tenantSchemaPattern.MatchString(schemaName) {
		return "", fmt.Errorf("refusing unsafe tenant schema name %q", schemaName)
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)`, schemaName).Scan(&exists); err != nil {
		return "", fmt.Errorf("verify tenant schema exists: %w", err)
	}
	if !exists {
		return "", ErrTenantNotRoutable
	}
	return schemaName, nil
}

func schemaNameForID(id string) string {
	return "tenant_" + strings.ReplaceAll(id, "-", "")
}

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate tenant identifier: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
