package tenantlifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/platform/tenantdb"
)

const PermissionExportTenant = "tenant.export"

var ErrForbidden = errors.New("tenant export forbidden")

type Service struct {
	router *tenantdb.Router
	clock  func() time.Time
}

func NewService(router *tenantdb.Router) *Service {
	return &Service{router: router, clock: time.Now}
}

type exportHeader struct {
	Type          string    `json:"type"`
	FormatVersion int       `json:"format_version"`
	TenantID      string    `json:"tenant_id"`
	ExportedAt    time.Time `json:"exported_at"`
}

type exportRow struct {
	Type  string          `json:"type"`
	Table string          `json:"table"`
	Row   json.RawMessage `json:"row"`
}

// ExportTenant streams a consistent logical JSONL data export for the active
// tenant. It is a user-authorized portability export, not a backup/restore path.
func (s *Service) ExportTenant(ctx context.Context, principal tenants.ActiveTenantContext, output io.Writer) error {
	if principal.UserID == "" || principal.Tenant.ID == "" || principal.Tenant.Status != auth.TenantActive ||
		output == nil || !contains(principal.Permissions, PermissionExportTenant) {
		return ErrForbidden
	}
	return s.router.WithTenantTxOptions(ctx, principal.Tenant.ID, &sql.TxOptions{
		Isolation: sql.LevelRepeatableRead,
		ReadOnly:  true,
	}, func(tx *sql.Tx) error {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(exportHeader{
			Type: "header", FormatVersion: 1, TenantID: principal.Tenant.ID, ExportedAt: s.clock().UTC(),
		}); err != nil {
			return fmt.Errorf("write export header: %w", err)
		}
		tableRows, err := tx.QueryContext(ctx, `
			SELECT table_name
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
			  AND table_name <> 'tenant_migrations'
			ORDER BY table_name`)
		if err != nil {
			return fmt.Errorf("list tenant export tables: %w", err)
		}
		var tables []string
		for tableRows.Next() {
			var name string
			if err := tableRows.Scan(&name); err != nil {
				_ = tableRows.Close()
				return fmt.Errorf("read tenant export table name: %w", err)
			}
			tables = append(tables, name)
		}
		if err := tableRows.Err(); err != nil {
			_ = tableRows.Close()
			return fmt.Errorf("iterate tenant export tables: %w", err)
		}
		if err := tableRows.Close(); err != nil {
			return fmt.Errorf("close tenant export table listing: %w", err)
		}

		for _, table := range tables {
			query := `SELECT row_to_json(export_row)::text FROM ` + quoteIdentifier(table) + ` AS export_row`
			rows, err := tx.QueryContext(ctx, query)
			if err != nil {
				return fmt.Errorf("read tenant export table %q: %w", table, err)
			}
			for rows.Next() {
				var row []byte
				if err := rows.Scan(&row); err != nil {
					_ = rows.Close()
					return fmt.Errorf("scan tenant export row from %q: %w", table, err)
				}
				if err := encoder.Encode(exportRow{Type: "row", Table: table, Row: json.RawMessage(row)}); err != nil {
					_ = rows.Close()
					return fmt.Errorf("write tenant export row from %q: %w", table, err)
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return fmt.Errorf("iterate tenant export table %q: %w", table, err)
			}
			if err := rows.Close(); err != nil {
				return fmt.Errorf("close tenant export table %q: %w", table, err)
			}
		}
		return nil
	})
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
