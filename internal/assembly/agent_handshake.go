package assembly

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/agents"
)

// AgentHandshakeAPI exposes agent capability negotiation over HTTP. Clients use
// it to discover what a credential is permitted to do instead of guessing.
type AgentHandshakeAPI struct {
	Service *agents.Service
	Agents  AgentCredentialLookup
}

type handshakeRequest struct {
	ProtocolVersion       string   `json:"protocol_version"`
	UserID                string   `json:"user_id"`
	SessionID             string   `json:"session_id"`
	ScopeType             string   `json:"scope_type"`
	RequestedCapabilities []string `json:"requested_capabilities"`
}

// Register mounts the handshake endpoint. It is a no-op when unconfigured.
func (h *AgentHandshakeAPI) Register(mux *http.ServeMux) {
	if h == nil || h.Service == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/agent/handshake", h.handle)
}

func (h *AgentHandshakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	// The credential travels as a Bearer token, never in the body: the body only
	// carries the requested scope and the optional bound user.
	secret := bearerValue(r)
	if secret == "" {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "an agent credential is required")
		return
	}
	var payload handshakeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", "handshake request is invalid")
		return
	}
	identity, err := resolveAgentCredential(r.Context(), h.Agents, secret, strings.TrimSpace(payload.UserID))
	if err != nil {
		switch {
		case errors.Is(err, ErrUserSelectionRequired):
			writeAPIError(w, http.StatusBadRequest, "USER_REQUIRED", "this agent binds multiple users; declare user_id")
		case errors.Is(err, agentaccess.ErrForbidden):
			writeAPIError(w, http.StatusForbidden, "FORBIDDEN", "the declared user is not permitted for this agent")
		default:
			writeAPIError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "the agent credential is invalid")
		}
		return
	}
	response, err := h.Service.Handshake(r.Context(), agents.HandshakeRequest{
		Credential:            secret,
		ProtocolVersion:       payload.ProtocolVersion,
		UserID:                identity.UserID,
		SessionID:             payload.SessionID,
		ScopeType:             payload.ScopeType,
		RequestedCapabilities: payload.RequestedCapabilities,
	})
	if err != nil {
		switch {
		case errors.Is(err, agents.ErrUnsupportedProtocol):
			writeAPIError(w, http.StatusBadRequest, "UNSUPPORTED_PROTOCOL", "unsupported agent protocol")
		case errors.Is(err, agents.ErrInvalidHandshake):
			writeAPIError(w, http.StatusUnauthorized, "INVALID_HANDSHAKE", "the handshake is not permitted for this credential")
		default:
			writeAPIError(w, http.StatusInternalServerError, "HANDSHAKE_FAILED", "unable to complete the handshake")
		}
		return
	}
	writeAPIData(w, http.StatusOK, response)
}
