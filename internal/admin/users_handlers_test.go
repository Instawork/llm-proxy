package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireRoleForbiddenForViewer(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "viewer@example.com")
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleAdmin, h.handleListUsers).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleListUsers(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	h.handleListUsers(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var users []AdminUserRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &users))
	assert.NotEmpty(t, users)
}

func TestHandleCreateUserRejectsBadDomain(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Email: "x@other.com", Role: "viewer"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleDeleteUserBlocksSelf(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/users/admin@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "admin@example.com"})
	rec := httptest.NewRecorder()
	h.handleDeleteUser(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleCreateKeyEditorCostCap(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "editor@example.com")
	body, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-real",
		DailyCostLimit: 999999,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
