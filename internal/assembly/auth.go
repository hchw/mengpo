package assembly

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/hchw/mengpo/internal/api/dto"
	"github.com/hchw/mengpo/internal/application/agentaccess"
	"github.com/hchw/mengpo/internal/application/identity"
	"github.com/hchw/mengpo/internal/application/tenants"
	"github.com/hchw/mengpo/internal/domain/auth"
)

// AuthAPI exposes the console authentication endpoints under /api/v1/auth/*.
type AuthAPI struct {
	Service      *identity.Service
	CookieName   string
	CookieSecure bool
}

// Register mounts the authentication routes. It is a no-op when the service is
// not configured so the rest of the API still serves.
func (a *AuthAPI) Register(mux *http.ServeMux) {
	if a == nil || a.Service == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/auth/sso/exchange", a.handleExchange)
	mux.HandleFunc("POST /api/v1/auth/register", a.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("GET /api/v1/auth/tenants", a.handleTenants)
	mux.HandleFunc("POST /api/v1/auth/tenant-context", a.handleTenantContext)
	mux.HandleFunc("POST /api/v1/auth/logout", a.handleLogout)
}

type registerRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type exchangeRequest struct {
	Assertion string `json:"assertion"`
}

type tenantOptionResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	MemberID  string   `json:"membership_id"`
	Roles     []string `json:"roles"`
	IsActive  bool     `json:"active"`
	SchemaSet bool     `json:"schema_ready"`
}

func (a *AuthAPI) handleExchange(w http.ResponseWriter, r *http.Request) {
	var body exchangeRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	result, err := a.Service.Exchange(r.Context(), body.Assertion)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, identity.ErrInvalidAssertion) {
			status = http.StatusBadRequest
		}
		writeAPIError(w, status, "AUTH_FAILED", "authentication failed")
		return
	}
	a.setCookie(w, result.Token)
	writeAPIData(w, http.StatusOK, authResponse(result))
}

func (a *AuthAPI) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body registerRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	result, err := a.Service.Register(r.Context(), identity.RegisterInput{
		Email:       body.Email,
		Password:    body.Password,
		DisplayName: body.DisplayName,
	})
	if err != nil {
		status, code := registerErrorStatus(err)
		writeAPIError(w, status, code, err.Error())
		return
	}
	a.setCookie(w, result.Token)
	writeAPIData(w, http.StatusOK, authResponse(result))
}

func (a *AuthAPI) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	result, err := a.Service.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		if errors.Is(err, identity.ErrInvalidCredentials) || errors.Is(err, identity.ErrAccountDisabled) {
			writeAPIError(w, http.StatusUnauthorized, "AUTH_FAILED", "email or password is incorrect")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "LOGIN_FAILED", "login failed")
		return
	}
	a.setCookie(w, result.Token)
	writeAPIData(w, http.StatusOK, authResponse(result))
}

// registerErrorStatus maps domain registration errors to HTTP responses.
func registerErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, identity.ErrInvalidEmail):
		return http.StatusBadRequest, "INVALID_EMAIL"
	case errors.Is(err, identity.ErrWeakPassword):
		return http.StatusBadRequest, "WEAK_PASSWORD"
	case errors.Is(err, auth.ErrEmailExists):
		return http.StatusConflict, "EMAIL_TAKEN"
	default:
		return http.StatusInternalServerError, "REGISTRATION_FAILED"
	}
}

func authResponse(result identity.AuthResult) map[string]any {
	return map[string]any{
		"user":    map[string]any{"id": result.User.ID, "email": result.User.Email, "display_name": result.User.DisplayName},
		"tenants": tenantOptions(result.Tenants),
	}
}

func (a *AuthAPI) handleTenants(w http.ResponseWriter, r *http.Request) {
	token := tokenFromRequest(r, a.CookieName)
	options, err := a.Service.ListTenants(r.Context(), token)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "SESSION_REQUIRED", "a verified session is required")
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{"tenants": tenantOptions(options)})
}

type tenantContextRequest struct {
	TenantID string `json:"tenant_id"`
}

func (a *AuthAPI) handleTenantContext(w http.ResponseWriter, r *http.Request) {
	var body tenantContextRequest
	if err := decodeJSONBody(r, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	active, err := a.Service.SelectTenant(r.Context(), tokenFromRequest(r, a.CookieName), body.TenantID)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, "TENANT_FORBIDDEN", "tenant is not accessible")
		return
	}
	writeAPIData(w, http.StatusOK, map[string]any{
		"tenant_id": active.Tenant.ID,
		"user_id":   active.UserID,
		"roles":     active.RoleIDs,
	})
}

func (a *AuthAPI) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = a.Service.Revoke(r.Context(), tokenFromRequest(r, a.CookieName))
	a.setCookie(w, "")
	writeAPIData(w, http.StatusOK, map[string]any{"logged_out": true})
}

func (a *AuthAPI) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// SessionAuthenticator resolves a console session token to a trusted identity.
type SessionAuthenticator struct {
	Service     *identity.Service
	CookieName  string
	Memberships tenants.MembershipRepository
}

func (a SessionAuthenticator) Authenticate(r *http.Request) (agentaccess.Identity, error) {
	if a.Service == nil {
		return agentaccess.Identity{}, identity.ErrSessionRequired
	}
	session, user, err := a.Service.Authenticate(r.Context(), tokenFromRequest(r, a.CookieName))
	if err != nil {
		return agentaccess.Identity{}, err
	}
	if session.TenantID == "" {
		return agentaccess.Identity{}, identity.ErrSessionRequired
	}
	var roles []string
	if a.Memberships != nil {
		if record, err := a.Memberships.GetMembership(r.Context(), user.ID, session.TenantID); err == nil {
			for _, role := range record.Roles {
				roles = append(roles, role.Name)
			}
		}
	}
	return agentaccess.Identity{
		TenantID:     session.TenantID,
		UserID:       user.ID,
		SourceID:     user.ID,
		Capabilities: []string{"observe", "project", "feedback"},
		Roles:        roles,
		Source:       agentaccess.SourceVerifiedUser,
	}, nil
}

func (a SessionAuthenticator) SelectTenant(ctx context.Context, token, tenantID string) (tenants.ActiveTenantContext, error) {
	if a.Service == nil {
		return tenants.ActiveTenantContext{}, identity.ErrSessionRequired
	}
	return a.Service.SelectTenant(ctx, token, tenantID)
}

func tokenFromRequest(r *http.Request, cookieName string) string {
	if header := r.Header.Get("Authorization"); len(header) > 7 && header[:7] == "Bearer " {
		return header[7:]
	}
	if cookieName == "" {
		cookieName = "memory_session"
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		return cookie.Value
	}
	return ""
}

func tenantOptions(options []tenants.TenantOption) []tenantOptionResponse {
	out := make([]tenantOptionResponse, 0, len(options))
	for _, option := range options {
		out = append(out, tenantOptionResponse{
			ID:        option.Tenant.ID,
			Name:      option.Tenant.Name,
			MemberID:  option.Membership.ID,
			Roles:     option.Roles,
			IsActive:  option.Tenant.Status == auth.TenantActive,
			SchemaSet: option.Tenant.Schema != "",
		})
	}
	return out
}

func decodeJSONBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request body is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("request body has trailing data")
	}
	return nil
}

func writeAPIData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"version": dto.CurrentVersion, "data": data})
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"version": dto.CurrentVersion, "error": map[string]any{"code": code, "message": message}})
}
