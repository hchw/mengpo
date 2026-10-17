package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hchw/mengpo/internal/platform/tenantdb"
	"github.com/hchw/mengpo/internal/ports"
)

// TenantCache is the PostgreSQL-backed implementation of ports.TenantCache. It
// replaces the external key/value store: entries live inside the tenant's own
// schema, and the physical key additionally embeds the tenant id, so two
// tenants can never observe each other's entries even for the same logical key.
type TenantCache struct {
	router *tenantdb.Router
}

// NewTenantCache builds a cache backed by the consolidate_cache table.
func NewTenantCache(router *tenantdb.Router) *TenantCache {
	return &TenantCache{router: router}
}

// Get returns the cached value when the key exists and has not expired. A miss
// is reported as (nil, false, nil), matching the replaceable-cache contract.
func (c *TenantCache) Get(ctx context.Context, tenantID, key string) ([]byte, bool, error) {
	if c == nil || c.router == nil {
		return nil, false, errors.New("tenant cache is not configured")
	}
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return nil, false, err
	}
	var (
		value []byte
		found bool
	)
	err = c.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		var payload []byte
		var active bool
		err := tx.QueryRowContext(ctx,
			`SELECT payload, expires_at > now() FROM consolidate_cache WHERE cache_key = $1`,
			physical,
		).Scan(&payload, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read consolidate cache: %w", err)
		}
		if !active {
			return nil
		}
		value = payload
		found = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return value, found, nil
}

// Put stores value under key with the given TTL. A non-positive TTL is rejected
// rather than silently producing an already-expired entry.
func (c *TenantCache) Put(ctx context.Context, tenantID, key string, value []byte, ttl time.Duration) error {
	if c == nil || c.router == nil {
		return errors.New("tenant cache is not configured")
	}
	if ttl <= 0 {
		return errors.New("consolidate cache put requires a positive ttl")
	}
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return err
	}
	expiresAt := time.Now().Add(ttl)
	return c.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO consolidate_cache (cache_key, payload, expires_at)
			VALUES ($1, $2::jsonb, $3)
			ON CONFLICT (cache_key) DO UPDATE
			SET payload = EXCLUDED.payload, expires_at = EXCLUDED.expires_at, created_at = now()`,
			physical, string(value), expiresAt)
		if err != nil {
			return fmt.Errorf("write consolidate cache: %w", err)
		}
		return nil
	})
}

// Delete removes a single entry. Deleting a missing key is not an error.
func (c *TenantCache) Delete(ctx context.Context, tenantID, key string) error {
	if c == nil || c.router == nil {
		return errors.New("tenant cache is not configured")
	}
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return err
	}
	return c.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM consolidate_cache WHERE cache_key = $1`, physical); err != nil {
			return fmt.Errorf("delete consolidate cache: %w", err)
		}
		return nil
	})
}

// PurgeExpired removes up to limit expired entries for the tenant and reports
// how many rows were deleted. It implements ports.ExpiredCachePurger.
func (c *TenantCache) PurgeExpired(ctx context.Context, tenantID string, limit int) (int, error) {
	if c == nil || c.router == nil {
		return 0, errors.New("tenant cache is not configured")
	}
	if limit <= 0 {
		limit = 500
	}
	var removed int
	err := c.router.WithTenantTx(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM consolidate_cache
			WHERE cache_key IN (
				SELECT cache_key FROM consolidate_cache
				WHERE expires_at <= now()
				LIMIT $1
			)`, limit)
		if err != nil {
			return fmt.Errorf("purge consolidate cache: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count purged consolidate cache: %w", err)
		}
		removed = int(count)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}
