package providerconfig

import (
	"context"
	"sync"
	"time"

	"github.com/hchw/mengpo/internal/application/analysis"
	"github.com/hchw/mengpo/internal/ports"
)

// Dynamic resolves a tenant's analyst from its stored configuration on demand
// and caches it briefly. This gives hot-swap semantics across processes: a
// worker that did not handle the save still picks up the change without a
// restart, and different tenants always use their own provider.
type Dynamic struct {
	Service *Service
	TTL     time.Duration
	Now     func() time.Time

	mu    sync.Mutex
	cache map[string]cachedAnalyst
}

type cachedAnalyst struct {
	analyst ports.MemoryAnalyst
	expires time.Time
}

// Analyze resolves the tenant's analyst and dispatches to it.
func (d *Dynamic) Analyze(ctx context.Context, batch ports.AnalysisBatch) (ports.AnalystResult, error) {
	analyst, err := d.analystFor(ctx, batch.TenantID)
	if err != nil {
		return ports.AnalystResult{}, err
	}
	return analyst.Analyze(ctx, batch)
}

// Invalidate drops a tenant's cached analyst so the next call reloads it.
func (d *Dynamic) Invalidate(tenantID string) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.cache, tenantID)
}

func (d *Dynamic) analystFor(ctx context.Context, tenantID string) (ports.MemoryAnalyst, error) {
	now := d.now()
	d.mu.Lock()
	if cached, ok := d.cache[tenantID]; ok && now.Before(cached.expires) {
		d.mu.Unlock()
		return cached.analyst, nil
	}
	d.mu.Unlock()

	cfg, err := d.Service.Effective(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	analyst, err := d.Service.Builder.Build(cfg)
	if err != nil {
		// A misconfigured provider must degrade, not fail the analysis.
		analyst = analysis.RuleFallback{}
	}
	ttl := d.TTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[string]cachedAnalyst{}
	}
	d.cache[tenantID] = cachedAnalyst{analyst: analyst, expires: now.Add(ttl)}
	d.mu.Unlock()
	return analyst, nil
}

func (d *Dynamic) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

var _ ports.MemoryAnalyst = (*Dynamic)(nil)
