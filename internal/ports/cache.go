package ports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrMissingTenantIdentity = errors.New("cache operation requires tenant identity")

// TenantCache is a tenant-scoped key/value cache. Every key is namespaced by the
// tenant id so two tenants can never read or overwrite each other's entries,
// even for the same logical key.
type TenantCache interface {
	Get(ctx context.Context, tenantID, key string) ([]byte, bool, error)
	Put(ctx context.Context, tenantID, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, tenantID, key string) error
}

// ExpiredCachePurger removes expired cache entries. It is a maintenance
// concern kept separate from the read/write TenantCache contract, so only the
// maintenance job depends on it.
type ExpiredCachePurger interface {
	PurgeExpired(ctx context.Context, tenantID string, limit int) (int, error)
}

// TenantCacheKey builds a physical cache key that always embeds the tenant id.
// An empty tenant is rejected rather than defaulting to a shared namespace.
func TenantCacheKey(tenantID, namespace, key string) (string, error) {
	if strings.TrimSpace(tenantID) == "" {
		return "", ErrMissingTenantIdentity
	}
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("%w: namespace and key are required", ErrInvalidProjectionRecord)
	}
	return "mengpo:tenant:" + tenantID + ":" + namespace + ":" + key, nil
}
