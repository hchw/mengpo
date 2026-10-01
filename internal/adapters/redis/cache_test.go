package redis

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/ports"
)

func testCache(t *testing.T) *Cache {
	t.Helper()
	url := os.Getenv("MEMORY_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set MEMORY_TEST_REDIS_URL to run Redis integration tests")
	}
	cache, err := NewCacheFromURL(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cache.client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	return cache
}

func TestTenantCacheKeyEmbedsTenantAndRejectsEmpty(t *testing.T) {
	key, err := ports.TenantCacheKey("tenant-a", "projection", "query-1")
	if err != nil {
		t.Fatal(err)
	}
	if key != "mengpo:tenant:tenant-a:projection:query-1" {
		t.Fatalf("key=%q", key)
	}
	other, _ := ports.TenantCacheKey("tenant-b", "projection", "query-1")
	if key == other {
		t.Fatal("keys for different tenants collided")
	}
	if _, err := ports.TenantCacheKey("", "projection", "query-1"); !errors.Is(err, ports.ErrMissingTenantIdentity) {
		t.Fatalf("empty tenant err=%v", err)
	}
}

func TestRedisCacheIsolatesTenantsForSameLogicalKey(t *testing.T) {
	cache := testCache(t)
	ctx := context.Background()
	if err := cache.Put(ctx, "tenant-a", "shared-key", []byte("a-value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := cache.Put(ctx, "tenant-b", "shared-key", []byte("b-value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cache.Delete(context.Background(), "tenant-a", "shared-key")
		_ = cache.Delete(context.Background(), "tenant-b", "shared-key")
	})
	a, ok, err := cache.Get(ctx, "tenant-a", "shared-key")
	if err != nil || !ok || string(a) != "a-value" {
		t.Fatalf("tenant-a read=%q ok=%v err=%v", a, ok, err)
	}
	b, ok, err := cache.Get(ctx, "tenant-b", "shared-key")
	if err != nil || !ok || string(b) != "b-value" {
		t.Fatalf("tenant-b read=%q ok=%v err=%v", b, ok, err)
	}
	if _, ok, _ := cache.Get(ctx, "tenant-c", "shared-key"); ok {
		t.Fatal("unknown tenant observed another tenant's value")
	}
	if err := cache.Delete(ctx, "tenant-a", "shared-key"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cache.Get(ctx, "tenant-a", "shared-key"); ok {
		t.Fatal("tenant-a value survived delete")
	}
	if _, ok, _ := cache.Get(ctx, "tenant-b", "shared-key"); !ok {
		t.Fatal("tenant-a delete removed tenant-b value")
	}
	if err := cache.Put(ctx, "tenant-a", "k", []byte("v"), 0); err == nil {
		t.Fatal("non-positive ttl accepted")
	}
}
