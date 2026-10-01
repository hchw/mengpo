package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/providerconfig"
	"github.com/hchw/mengpo/internal/ports"
)

// ConsoleOps serves the tenant-scoped operational endpoints that sit outside
// the memory read surface: per-tenant provider configuration, the curation
// schedule, and analysis run records. Writes are admin-only and audited; reads
// are available to any tenant member. Secrets are write-only.
type ConsoleOps struct {
	Providers      *providerconfig.Service
	Schedules      ScheduleAdmin
	ScheduleStatus func(ctx context.Context, tenantID string) ([]ports.ScheduleStatus, error)
	Runs           ports.AnalysisRunStore
	Audit          ports.AuditWriter
	Logger         *slog.Logger
}

// writeOpsError reports a failed operational call. An error carrying an API code
// (a rejected configuration) answers 400; anything else is a server-side fault,
// logged with its cause and reported as 500 without leaking details.
func (c *ConsoleOps) writeOpsError(w http.ResponseWriter, r *http.Request, err error, message string) {
	var coded interface{ APIErrorCode() string }
	if errors.As(err, &coded) {
		writeAPIError(w, http.StatusBadRequest, coded.APIErrorCode(), message)
		return
	}
	if c.Logger != nil {
		c.Logger.Error("console operation failed", slog.String("path", r.URL.Path), slog.String("error", err.Error()))
	}
	writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "operation failed")
}

// ScheduleAdmin reads and writes a tenant's own schedule overrides.
type ScheduleAdmin interface {
	Upsert(ctx context.Context, tenantID, name string, cadence time.Duration, enabled bool) error
}

// Register mounts the operational endpoints.
func (c *ConsoleOps) Register(mux *http.ServeMux) {
	if c == nil {
		return
	}
	if c.Providers != nil {
		mux.HandleFunc("POST /api/v1/providers", c.handleProviderView)
		mux.HandleFunc("PUT /api/v1/providers", c.handleProviderUpdate)
		mux.HandleFunc("POST /api/v1/providers/test", c.handleProviderTest)
	}
	if c.ScheduleStatus != nil {
		mux.HandleFunc("POST /api/v1/schedules", c.handleScheduleStatus)
	}
	if c.Schedules != nil {
		mux.HandleFunc("PUT /api/v1/schedules", c.handleScheduleUpdate)
	}
	if c.Runs != nil {
		mux.HandleFunc("POST /api/v1/analysis-runs", c.handleAnalysisRuns)
	}
}

type providerPayload struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

func (c *ConsoleOps) handleProviderView(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	view, err := c.Providers.Get(r.Context(), scoped.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to read provider configuration")
		return
	}
	writeAPIData(w, http.StatusOK, providerViewFor(view))
}

func (c *ConsoleOps) handleProviderUpdate(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if !callerIsAdmin(r) {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", "a tenant administrator is required")
		return
	}
	var payload providerPayload
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "provider payload is invalid")
			return
		}
	}
	view, err := c.Providers.Set(r.Context(), scoped.TenantID, scoped.UserID, providerconfig.Input{
		Enabled: payload.Enabled, BaseURL: payload.BaseURL, Model: payload.Model, APIKey: payload.APIKey,
	})
	if err != nil {
		c.writeOpsError(w, r, err, "provider configuration was rejected")
		return
	}
	c.recordAudit(r, scoped, envelope.RequestID, "provider_config.update", "provider_config", view.Provider, map[string]any{
		"enabled": view.Enabled, "base_url": view.BaseURL, "model": view.Model,
		"api_key": map[bool]string{true: "updated", false: "unchanged"}[strings.TrimSpace(payload.APIKey) != ""],
	})
	writeAPIData(w, http.StatusOK, providerViewFor(view))
}

func (c *ConsoleOps) handleProviderTest(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if !callerIsAdmin(r) {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", "a tenant administrator is required")
		return
	}
	var payload providerPayload
	if len(envelope.Payload) > 0 {
		_ = json.Unmarshal(envelope.Payload, &payload)
	}
	result, err := c.Providers.Test(r.Context(), scoped.TenantID, providerconfig.Input{
		Enabled: payload.Enabled, BaseURL: payload.BaseURL, Model: payload.Model, APIKey: payload.APIKey,
	})
	if err != nil {
		c.writeOpsError(w, r, err, "provider test input was rejected")
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"ok": result.OK, "latency_ms": result.Latency.Milliseconds(), "error": result.Error})
}

func (c *ConsoleOps) handleScheduleStatus(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	statuses, err := c.ScheduleStatus(r.Context(), scoped.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to read schedule status")
		return
	}
	items := make([]map[string]any, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, map[string]any{
			"name":            status.Name,
			"cadence_seconds": int(status.Cadence / time.Second),
			"next_run_at":     formatTime(status.NextRun),
			"last_run_at":     formatTime(status.LastRun),
			"last_status":     status.LastStatus,
			"last_error":      status.LastError,
			"runs":            status.Runs,
		})
	}
	writeAPIData(w, http.StatusOK, map[string]any{"items": items})
}

type schedulePayload struct {
	Name           string `json:"name"`
	CadenceSeconds int    `json:"cadence_seconds"`
	Enabled        bool   `json:"enabled"`
}

func (c *ConsoleOps) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	if !callerIsAdmin(r) {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", "a tenant administrator is required")
		return
	}
	var payload schedulePayload
	if len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "schedule payload is invalid")
			return
		}
	}
	if payload.Name == "" || payload.CadenceSeconds <= 0 {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "schedule requires a name and positive cadence")
		return
	}
	if err := c.Schedules.Upsert(r.Context(), scoped.TenantID, payload.Name, time.Duration(payload.CadenceSeconds)*time.Second, payload.Enabled); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "WRITE_FAILED", "unable to update the schedule")
		return
	}
	c.recordAudit(r, scoped, envelope.RequestID, "schedule.update", "schedule", payload.Name, map[string]any{
		"cadence_seconds": payload.CadenceSeconds, "enabled": payload.Enabled,
	})
	writeAPIData(w, http.StatusOK, map[string]any{"name": payload.Name, "cadence_seconds": payload.CadenceSeconds, "enabled": payload.Enabled})
}

type analysisRunPayload struct {
	SessionID string `json:"session_id"`
	TaskType  string `json:"task_type"`
	Status    string `json:"status"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
}

func (c *ConsoleOps) handleAnalysisRuns(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := bindConsoleRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	var payload analysisRunPayload
	if len(envelope.Payload) > 0 {
		_ = json.Unmarshal(envelope.Payload, &payload)
	}
	runs, err := c.Runs.ListAnalysisRuns(r.Context(), scoped.TenantID, ports.AnalysisRunFilter{
		SessionID: payload.SessionID, TaskType: payload.TaskType, Status: payload.Status,
		Page: payload.Page, PageSize: payload.PageSize,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to list analysis runs")
		return
	}
	items := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		items = append(items, map[string]any{
			"id":                run.ID,
			"task_type":         run.TaskType,
			"trigger":           run.Trigger,
			"provider":          run.Provider,
			"model":             run.Model,
			"prompt_version":    run.PromptVersion,
			"status":            run.Status,
			"latency_ms":        run.LatencyMS,
			"tokens_prompt":     run.TokensPrompt,
			"tokens_completion": run.TokensCompletion,
			"candidate_count":   run.CandidateCount,
			"discarded_count":   run.DiscardedCount,
			"conflict_count":    run.ConflictCount,
			"degraded_reason":   run.DegradedReason,
			"last_error":        run.LastError,
			"created_at":        run.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeAPIData(w, http.StatusOK, map[string]any{"items": items})
}

func (c *ConsoleOps) recordAudit(r *http.Request, scoped agentaccess.Scoped, requestID, action, resourceType, resourceID string, changes map[string]any) {
	if c.Audit == nil {
		return
	}
	encoded, err := json.Marshal(changes)
	if err != nil {
		return
	}
	_ = c.Audit.RecordAuditEvent(context.WithoutCancel(r.Context()), scoped.TenantID, ports.AuditEventRecord{
		ActorType: "user", ActorID: scoped.UserID, Action: action,
		ResourceType: resourceType, ResourceID: resourceID, RequestID: requestID, Changes: encoded,
	})
}

// bindConsoleRequest validates the envelope and the verified principal.
func bindConsoleRequest(r *http.Request) (dto.Envelope, agentaccess.Scoped, error) {
	envelope, err := dto.DecodeEnvelope(r.Body)
	if err != nil {
		return dto.Envelope{}, agentaccess.Scoped{}, err
	}
	identity, ok := agentaccess.IdentityFromContext(r.Context())
	if !ok || identity.TenantID == "" {
		return dto.Envelope{}, agentaccess.Scoped{}, errors.New("verified identity is required")
	}
	scoped, err := agentaccess.NewBinder().Bind(identity, envelope)
	if err != nil {
		return dto.Envelope{}, agentaccess.Scoped{}, err
	}
	return envelope, scoped, nil
}

func callerIsAdmin(r *http.Request) bool {
	identity, ok := agentaccess.IdentityFromContext(r.Context())
	if !ok {
		return false
	}
	for _, role := range identity.Roles {
		switch strings.ToLower(role) {
		case "owner", "admin", "administrator":
			return true
		}
	}
	return false
}

func providerViewFor(view providerconfig.View) map[string]any {
	return map[string]any{
		"provider":    view.Provider,
		"enabled":     view.Enabled,
		"base_url":    view.BaseURL,
		"model":       view.Model,
		"has_secret":  view.HasSecret,
		"secret_hint": view.SecretHint,
		"source":      view.Source,
		"updated_by":  view.UpdatedBy,
		"updated_at":  formatTime(view.UpdatedAt),
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
