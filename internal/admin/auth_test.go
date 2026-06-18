package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/sessions"
)

func TestHandleDevLogin_DisabledReturnsNotFound(t *testing.T) {
	store := sessions.NewCookieStore([]byte("test-secret-at-least-32-bytes-long"))
	auth := &authenticator{
		sessionStore: store,
		devBypass:    false,
		logger:       testLogger(),
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", nil)
	rec := httptest.NewRecorder()
	auth.handleDevLogin(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDevLogin_SetsSession(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "dev@example.com")

	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
		DevCORSOrigin:  "http://localhost:5173",
	}, nil)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"redirect": "http://localhost:5173/admin/",
	})
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	auth.handleDevLogin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%q", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["email"] != "dev@example.com" {
		t.Fatalf("unexpected email: %q", resp["email"])
	}

	req2 := httptest.NewRequest(http.MethodGet, "/admin/api/me", nil)
	for _, c := range rec.Result().Cookies() {
		req2.AddCookie(c)
	}
	user, err := auth.currentUser(req2)
	if err != nil {
		t.Fatalf("currentUser: %v", err)
	}
	if user.Email != "dev@example.com" {
		t.Fatalf("session email: %q", user.Email)
	}
}

func TestHandleDevLogin_ExplicitRoleInvalid(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")

	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
	}, nil)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"role": "superuser"})
	req := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	auth.handleDevLogin(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandleDevLogin_DevEmailDefaultsToAdmin(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")

	userStore := testAdminUserStore(t)
	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
	}, userStore)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", nil)
	rec := httptest.NewRecorder()
	auth.handleDevLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	user, err := userStore.GetUser(context.Background(), "dev@example.com")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Role != adminusers.RoleAdmin {
		t.Fatalf("got %q want admin", user.Role)
	}
}

func TestHandleDevLogin_PreservesExistingAdminRole(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "admin@example.com")

	userStore := testAdminUserStore(t)
	_, err := userStore.CreateUser(context.Background(), "admin@example.com", adminusers.RoleAdmin)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
	}, userStore)
	if err != nil {
		t.Fatalf("newAuthenticator: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", nil)
	rec := httptest.NewRecorder()
	auth.handleDevLogin(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	user, err := userStore.GetUser(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if user.Role != adminusers.RoleAdmin {
		t.Fatalf("got %q want admin", user.Role)
	}
}

func testLogger() *slog.Logger {
	return slog.Default()
}
