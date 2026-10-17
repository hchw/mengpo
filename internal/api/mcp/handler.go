package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "mengpo-memory"
	serverVersion = "0.1.0"
)

// Handler serves the MCP tool surface over streamable HTTP. UseCases and Resolve
// are required; a nil or failing Resolve rejects every request.
type Handler struct {
	UseCases UseCases
	Resolve  ResolveFunc
}

// HTTPHandler returns the streamable HTTP MCP handler. Every request is
// authenticated from the Authorization header before the MCP transport runs, so
// the transport never sees an unauthenticated caller. Tool availability is
// decided per session from the resolved identity's capabilities.
func (h *Handler) HTTPHandler() http.Handler {
	inner := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server {
		identity, _ := IdentityFromContext(r.Context())
		return h.buildServer(identity)
	}, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := bearerCredential(r)
		if credential == "" || h.Resolve == nil {
			writeUnauthorized(w)
			return
		}
		identity, err := h.Resolve(r.Context(), credential)
		if err != nil || identity.TenantID == "" {
			writeUnauthorized(w)
			return
		}
		inner.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), identity)))
	})
}

// buildServer registers only the tools the identity's capabilities permit.
func (h *Handler) buildServer(identity Identity) *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: serverName, Version: serverVersion}, nil)
	if identity.HasCapability("project") {
		sdkmcp.AddTool(server, recallTool(), h.recall(identity))
	}
	if identity.HasCapability("observe") {
		sdkmcp.AddTool(server, rememberTool(), h.remember(identity))
		sdkmcp.AddTool(server, forgetTool(), h.forget(identity))
		sdkmcp.AddTool(server, observeTool(), h.observe(identity))
	}
	if identity.HasCapability("feedback") {
		sdkmcp.AddTool(server, correctTool(), h.correct(identity))
	}
	if identity.HasCapability("session") {
		sdkmcp.AddTool(server, bindSessionTool(), h.bindSession(identity))
	}
	if identity.HasCapability("consolidate") {
		sdkmcp.AddTool(server, consolidateTool(), h.consolidate(identity))
	}
	return server
}

func bearerCredential(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "Bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="mengpo"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "mcp-request"
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return hex.EncodeToString(value[0:4]) + "-" + hex.EncodeToString(value[4:6]) + "-" + hex.EncodeToString(value[6:8]) + "-" + hex.EncodeToString(value[8:10]) + "-" + hex.EncodeToString(value[10:16])
}
