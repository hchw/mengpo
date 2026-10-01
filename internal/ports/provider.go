package ports

import (
	"context"
	"encoding/json"
	"time"
)

// ProviderConfigRecord is a tenant's stored provider configuration. The API key
// is carried as ciphertext; callers must never persist or log plaintext.
type ProviderConfigRecord struct {
	Provider         string
	Enabled          bool
	BaseURL          string
	Model            string
	Settings         json.RawMessage
	SecretCiphertext []byte
	KeyVersion       int
	UpdatedBy        string
	UpdatedAt        time.Time
}

// ProviderConfigStore persists per-tenant provider configuration in the tenant
// schema, so it is isolated per tenant and destroyed with the tenant.
type ProviderConfigStore interface {
	Get(ctx context.Context, tenantID, provider string) (ProviderConfigRecord, bool, error)
	Upsert(ctx context.Context, tenantID string, record ProviderConfigRecord) error
	Delete(ctx context.Context, tenantID, provider string) error
}
