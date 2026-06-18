package admin

import (
	"bytes"
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

func authenticatedRequestAs(t *testing.T, h *handler, email, method, path string, body []byte) *http.Request {
	t.Helper()
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", email)
	return authenticatedRequest(t, h, method, path, body)
}

func TestRequireRoleForbiddenForViewer(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleAdmin, h.handleListUsers).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRequireRoleUnauthorizedWithoutSession(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleAdmin, h.handleListUsers).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
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

func TestHandleListUsersNilStore(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.UserStore = nil
	rec := httptest.NewRecorder()
	h.handleListUsers(rec, httptest.NewRequest(http.MethodGet, "/admin/api/users", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleGetUser(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/users/admin@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "admin@example.com"})
	rec := httptest.NewRecorder()
	h.handleGetUser(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var user AdminUserRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &user))
	assert.Equal(t, "admin@example.com", user.Email)
	assert.Equal(t, "admin", user.Role)
}

func TestHandleGetUserNotFound(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/users/nobody@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "nobody@example.com"})
	rec := httptest.NewRecorder()
	h.handleGetUser(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleCreateUserRejectsMissingEmail(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Role: "viewer"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateUserRejectsBadDomain(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Email: "x@other.com", Role: "viewer"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateUserHappyPath(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Email: "neweditor@example.com", Role: "editor"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var user AdminUserRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &user))
	assert.Equal(t, "neweditor@example.com", user.Email)
	assert.Equal(t, "editor", user.Role)
}

func TestHandleCreateUserDuplicate(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Email: "admin@example.com", Role: "viewer"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleCreateUserInvalidRole(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(CreateUserRequest{Email: "x@example.com", Role: "superuser"})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/users", body)
	rec := httptest.NewRecorder()
	h.handleCreateUser(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateUserRole(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "promote@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	body, _ := json.Marshal(UpdateUserRoleRequest{Role: "editor"})
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/users/promote@example.com", body)
	req = mux.SetURLVars(req, map[string]string{"email": "promote@example.com"})
	rec := httptest.NewRecorder()
	h.handleUpdateUserRole(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var user AdminUserRecord
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &user))
	assert.Equal(t, "editor", user.Role)
}

func TestHandleUpdateUserRoleNotFound(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(UpdateUserRoleRequest{Role: "editor"})
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/users/nobody@example.com", body)
	req = mux.SetURLVars(req, map[string]string{"email": "nobody@example.com"})
	rec := httptest.NewRecorder()
	h.handleUpdateUserRole(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleUpdateUserRoleBlocksSelfDemote(t *testing.T) {
	h, _ := testAdminHandler(t)
	body, _ := json.Marshal(UpdateUserRoleRequest{Role: "editor"})
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/users/admin@example.com", body)
	req = mux.SetURLVars(req, map[string]string{"email": "admin@example.com"})
	rec := httptest.NewRecorder()
	h.handleUpdateUserRole(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleUpdateUserRoleBlocksLastAdminDemotion(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()

	_, err := h.deps.UserStore.CreateUser(ctx, "other@example.com", adminusers.RoleAdmin)
	require.NoError(t, err)
	require.NoError(t, h.deps.UserStore.DeleteUser(ctx, "admin@example.com"))

	body, _ := json.Marshal(UpdateUserRoleRequest{Role: "viewer"})
	req := authenticatedRequestAs(t, h, "other@example.com", http.MethodPatch, "/admin/api/users/other@example.com", body)
	req = mux.SetURLVars(req, map[string]string{"email": "other@example.com"})
	rec := httptest.NewRecorder()
	h.handleUpdateUserRole(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot demote your own admin account")
}

func TestHandleDeleteUserBlocksSelf(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/users/admin@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "admin@example.com"})
	rec := httptest.NewRecorder()
	h.handleDeleteUser(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleDeleteUserSuccess(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "remove@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/users/remove@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "remove@example.com"})
	rec := httptest.NewRecorder()
	h.handleDeleteUser(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestHandleDeleteUserAllowsDeletingViewerWithMultipleAdmins(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()

	_, err := h.deps.UserStore.CreateUser(ctx, "actor@example.com", adminusers.RoleAdmin)
	require.NoError(t, err)
	_, err = h.deps.UserStore.CreateUser(ctx, "remove@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "actor@example.com", http.MethodDelete, "/admin/api/users/remove@example.com", nil)
	req = mux.SetURLVars(req, map[string]string{"email": "remove@example.com"})
	rec := httptest.NewRecorder()
	h.handleDeleteUser(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestHandleMeIncludesRoleAndEditorLimits(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodGet, "/admin/api/me", nil)
	rec := httptest.NewRecorder()
	h.handleMe(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var user UserResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &user))
	assert.Equal(t, "editor", user.Role)
	require.NotNil(t, user.EditorLimits)
	assert.Equal(t, int64(5000), user.EditorLimits.MaxDailyCostLimitCents)
}

func TestHandleCreateKeyEditorCostCap(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-real",
		DailyCostLimit: 999999,
	})
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateKeyEditorCostCap(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	key, err := store.CreateKey(ctx, "openai", "sk", "", 1000, nil, nil)
	require.NoError(t, err)

	body := []byte(`{"daily_cost_limit": 999999}`)
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleDeleteKeyForbiddenForEditor(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodDelete, "/admin/api/keys/"+key.PK, nil)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleAdmin, h.handleDeleteKey).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleConfigForbiddenForViewer(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/config", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleEditor, h.handleConfig).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleDevLoginEnsuresUser(t *testing.T) {
	h, _ := testAdminHandler(t)
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "fresh@example.com")

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	h.auth.handleDevLogin(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	user, err := h.deps.UserStore.GetUser(context.Background(), "fresh@example.com")
	require.NoError(t, err)
	assert.Equal(t, adminusers.RoleViewer, user.Role)
}

func TestHandleDevLoginDefaultDevEmailIsAdmin(t *testing.T) {
	h, _ := testAdminHandler(t)
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "dev@example.com")

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	h.auth.handleDevLogin(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	user, err := h.deps.UserStore.GetUser(context.Background(), "dev@example.com")
	require.NoError(t, err)
	assert.Equal(t, adminusers.RoleAdmin, user.Role)
}

func TestHandleDevLoginExplicitRoleOverridesDefault(t *testing.T) {
	h, _ := testAdminHandler(t)
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "dev@example.com")

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{
		"redirect": "http://localhost:9002/admin/",
		"role":     "viewer",
	})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	h.auth.handleDevLogin(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	user, err := h.deps.UserStore.GetUser(context.Background(), "dev@example.com")
	require.NoError(t, err)
	assert.Equal(t, adminusers.RoleViewer, user.Role)
}
