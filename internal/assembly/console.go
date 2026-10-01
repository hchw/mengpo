package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/governance"
	"github.com/hchw/mengpo/internal/domain/auth"
	"github.com/hchw/mengpo/internal/domain/memory"
	"github.com/hchw/mengpo/internal/ports"
)

// ConsoleAPI serves the tenant-scoped read and admin endpoints the React console
// uses. Every request is a v1 Envelope; scope and principal are re-validated
// against the verified identity before any repository call.
type ConsoleAPI struct {
	Memories   ports.MemoryNodeRepository
	List       ports.MemoryListRepository
	Merge      ports.MemoryMergeRepository
	Governance *governance.Service
	Members    MemberLister
	Agents     AgentLister
}

// MemberLister lists tenant members.
type MemberLister interface {
	ListMembersForTenant(ctx context.Context, tenantID string) ([]ports.MemberRecord, error)
}

// AgentLister lists and disables agents.
type AgentLister interface {
	ListAgents(ctx context.Context, tenantID string) ([]auth.Agent, error)
	DisableAgent(ctx context.Context, agentID string) error
}

// Register mounts the console endpoints. It is a no-op when unconfigured.
func (c *ConsoleAPI) Register(mux *http.ServeMux) {
	if c == nil || c.Memories == nil || c.List == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/memories", c.handleMemories)
	mux.HandleFunc("POST /api/v1/memories/{id}", c.handleMemory)
	mux.HandleFunc("POST /api/v1/candidates", c.handleCandidates)
	mux.HandleFunc("POST /api/v1/candidates/{id}/{action}", c.handleCandidateAction)
	mux.HandleFunc("POST /api/v1/failures", c.handleFailures)
	mux.HandleFunc("POST /api/v1/evaluation", c.handleEvaluation)
	if c.Members != nil {
		mux.HandleFunc("POST /api/v1/members", c.handleMembers)
	}
	if c.Agents != nil {
		mux.HandleFunc("POST /api/v1/agents", c.handleAgents)
		mux.HandleFunc("POST /api/v1/agents/{id}/disable", c.handleDisableAgent)
	}
}

type pagePayload struct {
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
}

type memoryItem struct {
	ID             string  `json:"id"`
	Type           string  `json:"type"`
	Status         string  `json:"status"`
	ScopeType      string  `json:"scope_type"`
	ContentSummary string  `json:"content_summary"`
	Confidence     float64 `json:"confidence"`
	UpdatedAt      string  `json:"updated_at"`
}

type pageResponse struct {
	Items    []memoryItem `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"page_size"`
}

func (c *ConsoleAPI) handleMemories(w http.ResponseWriter, r *http.Request) {
	c.listMemories(w, r, false)
}

func (c *ConsoleAPI) handleCandidates(w http.ResponseWriter, r *http.Request) {
	c.listMemories(w, r, true)
}

func (c *ConsoleAPI) listMemories(w http.ResponseWriter, r *http.Request, candidatesOnly bool) {
	envelope, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	var payload pagePayload
	if len(envelope.Payload) > 0 {
		_ = json.Unmarshal(envelope.Payload, &payload)
	}
	statuses := []string{"active", "stable", "conflicted"}
	if candidatesOnly {
		statuses = []string{"candidate"}
	}
	result, err := c.List.ListMemories(r.Context(), scoped.TenantID, ports.MemoryListRequest{
		UserID: scoped.UserID, SessionID: scoped.SessionID, Statuses: statuses, Page: payload.Page, PageSize: payload.PageSize,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to read memories")
		return
	}
	items := make([]memoryItem, 0, len(result.Items))
	for _, node := range result.Items {
		items = append(items, memoryItem{
			ID:             node.ID,
			Type:           node.MemoryType,
			Status:         node.Status,
			ScopeType:      node.ScopeType,
			ContentSummary: summarize(node.Content, node.ContentText),
			Confidence:     node.Confidence,
			UpdatedAt:      node.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeAPIData(w, http.StatusOK, pageResponse{Items: items, Total: result.Total, Page: result.Page, PageSize: result.PageSize})
}

func (c *ConsoleAPI) handleCandidateAction(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	memoryID := r.PathValue("id")
	action := r.PathValue("action")
	if memoryID == "" || action == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "candidate id and action are required")
		return
	}
	node, err := c.Memories.Get(r.Context(), scoped.TenantID, memoryID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "candidate not found")
		return
	}
	switch action {
	case "confirm":
		if node.Status == "candidate" {
			node.Status = "active"
		}
		node.Confidence = 1
		node.DefaultRetrieval = true
		if _, err := c.Memories.Update(r.Context(), scoped.TenantID, node, node.Version); err != nil {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "unable to confirm candidate")
			return
		}
	case "correct":
		current, err := governance.FromRecord(node)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_STATE", "candidate cannot be corrected")
			return
		}
		var payload struct {
			ContentSummary string `json:"content_summary"`
			ContentText    string `json:"content_text"`
			RelationID     string `json:"relation_id"`
		}
		_ = json.Unmarshal(envelope.Payload, &payload)
		replacement := current
		replacement.ID, err = newUUID()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "unable to build correction")
			return
		}
		replacement.IdempotencyKey, _ = newUUID()
		if strings.TrimSpace(payload.ContentText) != "" {
			replacement.ContentText = payload.ContentText
		}
		if strings.TrimSpace(payload.ContentSummary) != "" {
			replacement.Content = json.RawMessage(`{"summary":` + strconv.Quote(payload.ContentSummary) + `}`)
		}
		relationID := payload.RelationID
		if relationID == "" {
			relationID, _ = newUUID()
		}
		auditID, err := newUUID()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "unable to audit correction")
			return
		}
		if _, err := c.Governance.Apply(r.Context(), scoped.TenantID, memoryID, memory.GovernanceCommand{
			Action:          memory.GovernanceCorrect,
			ExpectedVersion: node.Version,
			AuditID:         auditID,
			ActorType:       "user",
			ActorID:         scoped.UserID,
			RequestID:       envelope.RequestID,
			At:              time.Now().UTC(),
			RelationID:      relationID,
			Replacement:     &replacement,
		}); err != nil {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "unable to apply correction")
			return
		}
	case "reject", "expire", "delete":
		governanceAction := map[string]memory.GovernanceAction{"reject": memory.GovernanceReject, "expire": memory.GovernanceExpire, "delete": memory.GovernanceDelete}[action]
		newID := newUUID
		auditID, err := newID()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "unable to audit action")
			return
		}
		if _, err := c.Governance.Apply(r.Context(), scoped.TenantID, memoryID, memory.GovernanceCommand{
			Action:          governanceAction,
			ExpectedVersion: node.Version,
			AuditID:         auditID,
			ActorType:       "user",
			ActorID:         scoped.UserID,
			RequestID:       envelope.RequestID,
			At:              time.Now().UTC(),
		}); err != nil {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "unable to apply governance action")
			return
		}
	case "merge":
		if c.Merge == nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL", "merge is not configured")
			return
		}
		var payload struct {
			TargetMemoryID     string   `json:"target_memory_id"`
			DuplicateMemoryIDs []string `json:"duplicate_memory_ids"`
		}
		_ = json.Unmarshal(envelope.Payload, &payload)
		target := payload.TargetMemoryID
		if target == "" {
			target = memoryID
		}
		duplicates := payload.DuplicateMemoryIDs
		if len(duplicates) == 0 && target != memoryID {
			duplicates = []string{memoryID}
		}
		if target == "" || len(duplicates) == 0 {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "merge requires a target and at least one duplicate")
			return
		}
		result, err := c.Merge.MergeMemories(r.Context(), scoped.TenantID, ports.MergeRequest{
			TargetID: target, DuplicateIDs: duplicates, ActorType: "user", ActorID: scoped.UserID, RequestID: envelope.RequestID, At: time.Now().UTC(),
		})
		if err != nil {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "unable to merge candidates")
			return
		}
		writeAPIData(w, http.StatusOK, itemFor(result.Target))
		return
	default:
		writeAPIError(w, http.StatusBadRequest, "UNSUPPORTED_ACTION", "unsupported candidate action")
		return
	}
	writeAPIData(w, http.StatusOK, itemFor(node))
}

func (c *ConsoleAPI) handleMemory(w http.ResponseWriter, r *http.Request) {
	envelope, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	_ = envelope
	memoryID := r.PathValue("id")
	node, err := c.Memories.Get(r.Context(), scoped.TenantID, memoryID)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "memory not found")
		return
	}
	item := itemFor(node)
	item["provenance"] = node.Provenance
	writeAPIData(w, http.StatusOK, item)
}

func (c *ConsoleAPI) handleFailures(w http.ResponseWriter, r *http.Request) {
	if _, _, err := c.bind(r); err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	// Failure analysis has no persisted projection yet; return an explicit empty
	// page rather than fabricating data.
	writeAPIData(w, http.StatusOK, map[string]any{"items": []any{}, "total": 0, "page": 1, "page_size": 20})
}

func (c *ConsoleAPI) handleEvaluation(w http.ResponseWriter, r *http.Request) {
	if _, _, err := c.bind(r); err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	// Evaluation is produced offline; expose a zeroed snapshot with an explicit
	// generated_at so the console can render "no data yet".
	writeAPIData(w, http.StatusOK, map[string]any{
		"generated_at":        time.Now().UTC().Format(time.RFC3339),
		"retrieval_precision": 0, "promotion_precision": 0, "wrong_memory_rate": 0,
		"attribution_accuracy": 0, "latency_ms_p95": 0, "tokens_per_projection": 0,
		"cost_usd": 0, "cache_hit_rate": 0,
	})
}

func (c *ConsoleAPI) handleMembers(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	members, err := c.Members.ListMembersForTenant(r.Context(), scoped.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to list members")
		return
	}
	items := make([]map[string]any, 0, len(members))
	for _, member := range members {
		items = append(items, map[string]any{"user_id": member.UserID, "email": member.Email, "role": member.Role, "status": member.Status})
	}
	writeAPIData(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": len(items)})
}

func (c *ConsoleAPI) handleAgents(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	agents, err := c.Agents.ListAgents(r.Context(), scoped.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "READ_FAILED", "unable to list agents")
		return
	}
	items := make([]map[string]any, 0, len(agents))
	for _, agent := range agents {
		items = append(items, map[string]any{"id": agent.ID, "name": agent.Name, "status": agent.Status, "capabilities": agent.Capabilities, "allowed_scopes": agent.AllowedScopes})
	}
	writeAPIData(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "page": 1, "page_size": len(items)})
}

func (c *ConsoleAPI) handleDisableAgent(w http.ResponseWriter, r *http.Request) {
	_, scoped, err := c.bind(r)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		return
	}
	agentID := r.PathValue("id")
	if agentID == "" {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "agent id is required")
		return
	}
	if err := c.Agents.DisableAgent(r.Context(), agentID); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "MUTATION_FAILED", "unable to disable agent")
		return
	}
	agents, _ := c.Agents.ListAgents(r.Context(), scoped.TenantID)
	for _, agent := range agents {
		if agent.ID == agentID {
			writeAPIData(w, http.StatusOK, map[string]any{"id": agent.ID, "name": agent.Name, "status": agent.Status, "capabilities": agent.Capabilities, "allowed_scopes": agent.AllowedScopes})
			return
		}
	}
	writeAPIData(w, http.StatusOK, map[string]any{"id": agentID, "status": "disabled"})
}

func (c *ConsoleAPI) bind(r *http.Request) (dto.Envelope, agentaccess.Scoped, error) {
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

func itemFor(node ports.MemoryNodeRecord) map[string]any {
	return map[string]any{
		"id":              node.ID,
		"type":            node.MemoryType,
		"status":          node.Status,
		"scope_type":      node.ScopeType,
		"content_summary": summarize(node.Content, node.ContentText),
		"confidence":      node.Confidence,
		"updated_at":      node.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func summarize(content json.RawMessage, contentText string) string {
	if len(content) > 0 {
		var fields map[string]any
		if json.Unmarshal(content, &fields) == nil {
			for _, key := range []string{"summary", "abstract", "text"} {
				if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
					return value
				}
			}
		}
	}
	if len(contentText) > 200 {
		return contentText[:200]
	}
	return contentText
}
