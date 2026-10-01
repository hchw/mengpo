package assembly

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/hchw/mengpo/internal/config"
	"github.com/hchw/mengpo/internal/platform/registry"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func randomMemberID(t *testing.T) string {
	t.Helper()
	var value [16]byte
	for i := range value {
		value[i] = byte(i + 1)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return stringHex(value)
}

func stringHex(value [16]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range value {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[b>>4], hexdigits[b&0x0f])
	}
	return string(out)
}

// TestAuthFlow exercises SSO exchange, session cookie resolution and tenant
// selection over real HTTP against PostgreSQL.
func TestAuthFlow(t *testing.T) {
	dsn := os.Getenv("MEMORY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEMORY_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(db)
	tenant := createMigratedTenant(t, ctx, db, store, "auth flow")
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+tenant.Schema+` CASCADE`)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.tenants WHERE id=$1`, tenant.ID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM public.users WHERE email='owner@example.com'`)
	})

	cfg := config.Config{
		Environment: "development",
		Auth:        config.AuthConfig{SessionCookieName: "memory_session", SessionTTL: time.Hour},
		Security:    config.SecurityConfig{MaxRequestBytes: 1 << 20},
	}
	server, err := NewServer(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	// Exchange creates the user and issues a session cookie.
	exchange := httptest.NewRecorder()
	exchangeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/sso/exchange", bytes.NewBufferString(`{"assertion":"owner@example.com"}`))
	exchangeRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(exchange, exchangeRequest)
	if exchange.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", exchange.Code, exchange.Body.String())
	}
	cookies := exchange.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value == "" {
		t.Fatal("exchange did not set a session cookie")
	}
	token := cookies[0].Value
	var exchangeBody struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(exchange.Body.Bytes(), &exchangeBody); err != nil {
		t.Fatal(err)
	}
	userID := exchangeBody.Data.User.ID
	if userID == "" {
		t.Fatal("exchange did not return a user id")
	}

	// Grant membership, then the tenant list and selection must see it.
	if _, err := db.ExecContext(ctx, `INSERT INTO public.tenant_memberships (id, user_id, tenant_id, status) VALUES ($1::uuid,$2::uuid,$3::uuid,'active')`, randomMemberID(t), userID, tenant.ID); err != nil {
		t.Fatal(err)
	}

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/tenants", nil)
	listRequest.AddCookie(&http.Cookie{Name: "memory_session", Value: token})
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, listRequest)
	if list.Code != http.StatusOK {
		t.Fatalf("tenants status=%d body=%s", list.Code, list.Body.String())
	}
	if !bytes.Contains(list.Body.Bytes(), []byte(tenant.ID)) {
		t.Fatalf("tenant list missing tenant: %s", list.Body.String())
	}

	contextRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/tenant-context", bytes.NewBufferString(`{"tenant_id":"`+tenant.ID+`"}`))
	contextRequest.Header.Set("Content-Type", "application/json")
	contextRequest.AddCookie(&http.Cookie{Name: "memory_session", Value: token})
	selected := httptest.NewRecorder()
	handler.ServeHTTP(selected, contextRequest)
	if selected.Code != http.StatusOK {
		t.Fatalf("tenant-context status=%d body=%s", selected.Code, selected.Body.String())
	}
}
