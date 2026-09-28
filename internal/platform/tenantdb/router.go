package tenantdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

var ErrInvalidSchemaName = errors.New("invalid tenant schema name")

var safeSchemaName = regexp.MustCompile(`^tenant_[a-f0-9]{32}$`)

type SchemaResolver interface {
	ResolveSchema(ctx context.Context, tenantID string) (string, error)
}

type Router struct {
	db       *sql.DB
	resolver SchemaResolver
}

func NewRouter(db *sql.DB, resolver SchemaResolver) *Router {
	return &Router{db: db, resolver: resolver}
}

// WithTenantTx resolves the schema from the trusted tenant registry and applies
// a transaction-local search_path. The search_path is automatically restored by
// PostgreSQL on commit or rollback, preventing pooled-connection tenant leakage.
func (r *Router) WithTenantTx(ctx context.Context, tenantID string, operation func(*sql.Tx) error) error {
	return r.WithTenantTxOptions(ctx, tenantID, nil, operation)
}

func (r *Router) WithTenantTxOptions(ctx context.Context, tenantID string, options *sql.TxOptions, operation func(*sql.Tx) error) error {
	if tenantID == "" || operation == nil || r == nil || r.db == nil || r.resolver == nil {
		return ErrInvalidSchemaName
	}
	schemaName, err := r.resolver.ResolveSchema(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("resolve tenant schema: %w", err)
	}
	if !safeSchemaName.MatchString(schemaName) {
		return ErrInvalidSchemaName
	}
	tx, err := r.db.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin tenant transaction: %w", err)
	}
	defer tx.Rollback()

	// set_config is parameterized; true makes the change transaction-local.
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, schemaName+", public"); err != nil {
		return fmt.Errorf("set tenant search_path: %w", err)
	}
	if err := operation(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	return nil
}
