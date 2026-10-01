package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// ProviderConfigRepository stores per-tenant provider configuration in the
// tenant schema. The secret is stored as ciphertext and destroyed with the
// tenant schema.
type ProviderConfigRepository struct {
	router *tenantdb.Router
}

func NewProviderConfigRepository(router *tenantdb.Router) *ProviderConfigRepository {
	return &ProviderConfigRepository{router: router}
}

func (r *ProviderConfigRepository) Get(ctx context.Context, tenantID, provider string) (ports.ProviderConfigRecord, bool, error) {
	if tenantID == "" || provider == "" {
		return ports.ProviderConfigRecord{}, false, errors.New("provider config requires tenant and provider")
	}
	var record ports.ProviderConfigRecord
	found := false
	err := r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var settings []byte
		var ciphertext []byte
		var updatedBy sql.NullString
		err := tx.QueryRowContext(ctx, `
SELECT provider, enabled, base_url, model, settings, secret_ciphertext, key_version, updated_by, updated_at
FROM provider_configs WHERE provider = $1`, provider).
			Scan(&record.Provider, &record.Enabled, &record.BaseURL, &record.Model, &settings, &ciphertext, &record.KeyVersion, &updatedBy, &record.UpdatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("load provider config: %w", err)
		}
		found = true
		record.Settings = json.RawMessage(settings)
		record.SecretCiphertext = ciphertext
		record.UpdatedBy = updatedBy.String
		return nil
	})
	return record, found, err
}

func (r *ProviderConfigRepository) Upsert(ctx context.Context, tenantID string, record ports.ProviderConfigRecord) error {
	if tenantID == "" || record.Provider == "" {
		return errors.New("provider config requires tenant and provider")
	}
	settings := record.Settings
	if len(settings) == 0 {
		settings = json.RawMessage(`{}`)
	}
	if !json.Valid(settings) {
		settings = json.RawMessage(`{}`)
	}
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO provider_configs (provider, enabled, base_url, model, settings, secret_ciphertext, key_version, updated_by, updated_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, NULLIF($8, '')::uuid, now())
ON CONFLICT (provider) DO UPDATE SET
	enabled = EXCLUDED.enabled,
	base_url = EXCLUDED.base_url,
	model = EXCLUDED.model,
	settings = EXCLUDED.settings,
	secret_ciphertext = EXCLUDED.secret_ciphertext,
	key_version = EXCLUDED.key_version,
	updated_by = EXCLUDED.updated_by,
	updated_at = now()`,
			record.Provider, record.Enabled, record.BaseURL, record.Model, string(settings), record.SecretCiphertext, record.KeyVersion, record.UpdatedBy)
		if err != nil {
			return fmt.Errorf("upsert provider config: %w", err)
		}
		return nil
	})
}

func (r *ProviderConfigRepository) Delete(ctx context.Context, tenantID, provider string) error {
	return r.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM provider_configs WHERE provider = $1`, provider)
		return err
	})
}

var _ ports.ProviderConfigStore = (*ProviderConfigRepository)(nil)
