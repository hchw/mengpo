package observability

import (
	"context"
	"sync"
)

type tenantKey struct{}

type tenantHolderKey struct{}

// tenantHolder lets middleware read the tenant resolved later by a handler
// (identity is resolved inside the handler, after middleware starts).
type tenantHolder struct {
	mu sync.Mutex
	id string
}

func (h *tenantHolder) set(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.id = id
}

func (h *tenantHolder) get() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.id
}

func withTenantHolder(ctx context.Context) context.Context {
	return context.WithValue(ctx, tenantHolderKey{}, &tenantHolder{})
}

// WithTenant records the trusted tenant identity on the request context so
// downstream metrics, logs and caches can be tenant-attributed. It never reads
// a tenant from untrusted input.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	if holder, ok := ctx.Value(tenantHolderKey{}).(*tenantHolder); ok {
		holder.set(tenantID)
		return ctx
	}
	return context.WithValue(ctx, tenantKey{}, tenantID)
}

// Tenant returns the trusted tenant identity on the context, or "" when no
// trusted tenant has been resolved.
func Tenant(ctx context.Context) string {
	if holder, ok := ctx.Value(tenantHolderKey{}).(*tenantHolder); ok {
		return holder.get()
	}
	value, _ := ctx.Value(tenantKey{}).(string)
	return value
}
