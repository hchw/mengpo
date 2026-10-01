package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type contextKey uint8

const (
	requestIDKey contextKey = iota
	traceIDKey
)

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func TraceID(ctx context.Context) string {
	value, _ := ctx.Value(traceIDKey).(string)
	return value
}

type Metrics struct {
	requests     atomic.Uint64
	serverErrors atomic.Uint64
	byTenant     sync.Map // tenantID string -> *tenantCounters
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(w,
			"# HELP memory_http_requests_total Total HTTP requests handled.\n# TYPE memory_http_requests_total counter\nmemory_http_requests_total %d\n# HELP memory_http_server_errors_total HTTP 5xx responses.\n# TYPE memory_http_server_errors_total counter\nmemory_http_server_errors_total %d\n",
			m.requests.Load(), m.serverErrors.Load())
		for _, snapshot := range m.TenantSnapshots() {
			_, _ = fmt.Fprintf(w, "# HELP memory_http_tenant_requests_total Per-tenant HTTP requests handled.\n# TYPE memory_http_tenant_requests_total counter\nmemory_http_tenant_requests_total{tenant=%q} %d\n# HELP memory_http_tenant_server_errors_total Per-tenant HTTP 5xx responses.\n# TYPE memory_http_tenant_server_errors_total counter\nmemory_http_tenant_server_errors_total{tenant=%q} %d\n", snapshot.Tenant, snapshot.Requests, snapshot.Tenant, snapshot.ServerErrors)
		}
	})
}

func (m *Metrics) Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if !validRequestID(requestID) {
			var err error
			requestID, err = randomHex(16)
			if err != nil {
				http.Error(w, "unable to create request identifier", http.StatusInternalServerError)
				return
			}
		}
		traceID := traceIDFromParent(r.Header.Get("traceparent"))
		if traceID == "" {
			var err error
			traceID, err = randomHex(16)
			if err != nil {
				http.Error(w, "unable to create trace identifier", http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Trace-ID", traceID)

		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		ctx = context.WithValue(ctx, traceIDKey, traceID)
		ctx = withTenantHolder(ctx)
		started := time.Now()
		writer := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(writer, r.WithContext(ctx))
		if writer.status == 0 {
			writer.status = http.StatusOK
		}
		m.requests.Add(1)
		if writer.status >= 500 {
			m.serverErrors.Add(1)
		}
		if tenantID := Tenant(ctx); tenantID != "" {
			m.recordTenant(tenantID, writer.status)
		}
		logger.InfoContext(ctx, "http request",
			slog.String("request_id", requestID),
			slog.String("trace_id", traceID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", writer.status),
			slog.Int64("duration_ms", time.Since(started).Milliseconds()),
		)
	})
}

type tenantCounters struct {
	requests     atomic.Uint64
	serverErrors atomic.Uint64
}

type tenantSnapshot struct {
	Tenant       string
	Requests     uint64
	ServerErrors uint64
}

func (m *Metrics) recordTenant(tenantID string, status int) {
	value, _ := m.byTenant.LoadOrStore(tenantID, &tenantCounters{})
	counters := value.(*tenantCounters)
	counters.requests.Add(1)
	if status >= 500 {
		counters.serverErrors.Add(1)
	}
}

// TenantSnapshots returns per-tenant counters in a stable order.
func (m *Metrics) TenantSnapshots() []tenantSnapshot {
	var snapshots []tenantSnapshot
	m.byTenant.Range(func(key, value any) bool {
		counters := value.(*tenantCounters)
		snapshots = append(snapshots, tenantSnapshot{Tenant: key.(string), Requests: counters.requests.Load(), ServerErrors: counters.serverErrors.Load()})
		return true
	})
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Tenant < snapshots[j].Tenant })
	return snapshots
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func NewLogger(output io.Writer) *slog.Logger {
	if output == nil {
		output = os.Stdout
	}
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func HealthHandler(readinessCheck func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, http.StatusOK, "alive")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if readinessCheck == nil {
			writeHealth(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		if err := readinessCheck(r.Context()); err != nil {
			writeHealth(w, http.StatusServiceUnavailable, "not_ready")
			return
		}
		writeHealth(w, http.StatusOK, "ready")
	})
	return mux
}

func writeHealth(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{Status: state})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') &&
			!(char >= '0' && char <= '9') && char != '-' && char != '_' && char != '.' {
			return false
		}
	}
	return true
}

func traceIDFromParent(value string) string {
	parts := strings.Split(value, "-")
	if len(parts) != 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return ""
	}
	if _, err := hex.DecodeString(parts[1]); err != nil || parts[1] == strings.Repeat("0", 32) {
		return ""
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || parts[2] == strings.Repeat("0", 16) {
		return ""
	}
	if _, err := hex.DecodeString(parts[0] + parts[3]); err != nil {
		return ""
	}
	return strings.ToLower(parts[1])
}

func randomHex(bytes int) (string, error) {
	if bytes <= 0 {
		return "", errors.New("random identifier length must be positive")
	}
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
