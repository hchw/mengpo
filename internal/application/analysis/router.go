package analysis

import (
	"context"
	"sync"

	"github.com/hchw/mengpo/internal/ports"
)

// Router dispatches analysis to a per-tenant analyst, falling back to a shared
// default. It lets a tenant's provider configuration take effect at runtime
// (hot swap) without restarting the process or affecting other tenants. Privacy
// is applied by the wrapping Service before the batch reaches the router, so a
// per-tenant analyst only ever sees already-redacted input.
type Router struct {
	mu       sync.RWMutex
	fallback ports.MemoryAnalyst
	tenants  map[string]ports.MemoryAnalyst
}

// NewRouter builds a router with a shared fallback analyst.
func NewRouter(fallback ports.MemoryAnalyst) *Router {
	return &Router{fallback: fallback, tenants: map[string]ports.MemoryAnalyst{}}
}

// Set installs (or replaces) the analyst for one tenant.
func (r *Router) Set(tenantID string, analyst ports.MemoryAnalyst) {
	if r == nil || tenantID == "" || analyst == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants[tenantID] = analyst
}

// Clear removes a tenant's override so the shared fallback applies again.
func (r *Router) Clear(tenantID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tenants, tenantID)
}

// Analyze dispatches by the batch's trusted tenant identity.
func (r *Router) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	if r == nil {
		return RuleFallback{}.Analyze(ctx, batch)
	}
	r.mu.RLock()
	analyst, ok := r.tenants[batch.TenantID]
	fallback := r.fallback
	r.mu.RUnlock()
	if ok && analyst != nil {
		return analyst.Analyze(ctx, batch)
	}
	if fallback == nil {
		fallback = RuleFallback{}
	}
	return fallback.Analyze(ctx, batch)
}

// HasTenant reports whether a tenant has a dedicated analyst installed.
func (r *Router) HasTenant(tenantID string) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tenants[tenantID]
	return ok
}

var _ ports.MemoryAnalyst = (*Router)(nil)
