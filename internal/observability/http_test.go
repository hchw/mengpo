package observability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthHandlerReportsLivenessAndReadiness(t *testing.T) {
	handler := HealthHandler(func(context.Context) error { return nil })

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if live.Code != http.StatusOK || !strings.Contains(live.Body.String(), `"status":"alive"`) {
		t.Fatalf("liveness response = %d %s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"status":"ready"`) {
		t.Fatalf("readiness response = %d %s", ready.Code, ready.Body.String())
	}

	notReadyHandler := HealthHandler(func(context.Context) error { return errors.New("database unavailable") })
	notReady := httptest.NewRecorder()
	notReadyHandler.ServeHTTP(notReady, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if notReady.Code != http.StatusServiceUnavailable || !strings.Contains(notReady.Body.String(), "not_ready") {
		t.Fatalf("unready response = %d %s", notReady.Code, notReady.Body.String())
	}
}

func TestHealthHandlerWithoutReadinessCheckFailsClosed(t *testing.T) {
	recorder := httptest.NewRecorder()
	HealthHandler(nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz without dependency checks returned %d, want 503", recorder.Code)
	}
}

func TestMiddlewareSetsRequestAndTraceIDsAndRecordsMetrics(t *testing.T) {
	metrics := &Metrics{}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := metrics.Middleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) != "client-req-1" {
			t.Errorf("request id in context = %q", RequestID(r.Context()))
		}
		if TraceID(r.Context()) != "0123456789abcdef0123456789abcdef" {
			t.Errorf("trace id in context = %q", TraceID(r.Context()))
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	request := httptest.NewRequest(http.MethodGet, "/example?secret=must-not-be-logged", nil)
	request.Header.Set("X-Request-ID", "client-req-1")
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Header().Get("X-Request-ID") != "client-req-1" || recorder.Header().Get("X-Trace-ID") != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("response correlation headers = %#v", recorder.Header())
	}
	if metrics.requests.Load() != 1 || metrics.serverErrors.Load() != 1 {
		t.Fatalf("metrics requests=%d serverErrors=%d", metrics.requests.Load(), metrics.serverErrors.Load())
	}
	if strings.Contains(logs.String(), "secret=must-not-be-logged") {
		t.Fatal("logger must not record query parameters")
	}
}

func TestMiddlewareReplacesInvalidRequestID(t *testing.T) {
	handler := (&Metrics{}).Middleware(NewLogger(io.Discard), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validRequestID(RequestID(r.Context())) {
			t.Errorf("generated request ID is invalid: %q", RequestID(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Request-ID", "bad id\r\n")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !validRequestID(recorder.Header().Get("X-Request-ID")) {
		t.Fatalf("response = %d, request id = %q", recorder.Code, recorder.Header().Get("X-Request-ID"))
	}
}

func TestMetricsHandlerUsesLowCardinalityCounters(t *testing.T) {
	metrics := &Metrics{}
	metrics.requests.Store(5)
	metrics.serverErrors.Store(1)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "memory_http_requests_total 5") ||
		!strings.Contains(recorder.Body.String(), "memory_http_server_errors_total 1") {
		t.Fatalf("metrics response = %d %s", recorder.Code, recorder.Body.String())
	}
}
