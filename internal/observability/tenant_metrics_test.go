package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsAreLabeledPerTenantFromTrustedContext(t *testing.T) {
	metrics := &Metrics{}
	handler := metrics.Middleware(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The handler resolves the trusted tenant after middleware starts.
		if r.URL.Path == "/a" {
			WithTenant(r.Context(), "tenant-a")
		} else {
			WithTenant(r.Context(), "tenant-b")
			w.WriteHeader(http.StatusInternalServerError)
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/a", "/a", "/b"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	snapshots := metrics.TenantSnapshots()
	if len(snapshots) != 2 {
		t.Fatalf("expected two tenant series, got %#v", snapshots)
	}
	if snapshots[0].Tenant != "tenant-a" || snapshots[0].Requests != 2 || snapshots[0].ServerErrors != 0 {
		t.Fatalf("tenant-a snapshot=%#v", snapshots[0])
	}
	if snapshots[1].Tenant != "tenant-b" || snapshots[1].Requests != 1 || snapshots[1].ServerErrors != 1 {
		t.Fatalf("tenant-b snapshot=%#v", snapshots[1])
	}

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `memory_http_tenant_requests_total{tenant="tenant-a"} 2`) {
		t.Fatalf("tenant-a series missing:\n%s", body)
	}
	if !strings.Contains(body, `memory_http_tenant_server_errors_total{tenant="tenant-b"} 1`) {
		t.Fatalf("tenant-b error series missing:\n%s", body)
	}
	if !strings.Contains(body, "memory_http_requests_total 3") {
		t.Fatalf("global series missing:\n%s", body)
	}
}

func TestMetricsWithoutTenantAreNotAttributed(t *testing.T) {
	metrics := &Metrics{}
	handler := metrics.Middleware(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if snapshots := metrics.TenantSnapshots(); len(snapshots) != 0 {
		t.Fatalf("unattributed request created a tenant series: %#v", snapshots)
	}
}

func TestTenantContextFallsBackOutsideMiddleware(t *testing.T) {
	ctx := WithTenant(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "tenant-x")
	if got := Tenant(ctx); got != "tenant-x" {
		t.Fatalf("Tenant(ctx)=%q", got)
	}
	if got := Tenant(httptest.NewRequest(http.MethodGet, "/", nil).Context()); got != "" {
		t.Fatalf("empty context Tenant=%q", got)
	}
}
