// Package redis provides a tenant-scoped cache adapter. It is a replaceable
// transport: the domain depends only on ports.TenantCache. Every physical key
// embeds the tenant id so cross-tenant contamination is impossible even when
// two tenants use the same logical key.
package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/hchw/mengpo/internal/ports"
)

var ErrEmptyTenant = ports.ErrMissingTenantIdentity

type Cache struct {
	client *goredis.Client
}

func NewCache(client *goredis.Client) *Cache {
	return &Cache{client: client}
}

func NewCacheFromURL(url string) (*Cache, error) {
	options, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return NewCache(goredis.NewClient(options)), nil
}

func (c *Cache) Get(ctx context.Context, tenantID, key string) ([]byte, bool, error) {
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return nil, false, err
	}
	value, err := c.client.Get(ctx, physical).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("redis get: %w", err)
	}
	return value, true, nil
}

func (c *Cache) Put(ctx context.Context, tenantID, key string, value []byte, ttl time.Duration) error {
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		return errors.New("redis put requires a positive ttl")
	}
	if err := c.client.Set(ctx, physical, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}
	return nil
}

func (c *Cache) Delete(ctx context.Context, tenantID, key string) error {
	physical, err := ports.TenantCacheKey(tenantID, "cache", key)
	if err != nil {
		return err
	}
	if err := c.client.Del(ctx, physical).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}
	return nil
}
