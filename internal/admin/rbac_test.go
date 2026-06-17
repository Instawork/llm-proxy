package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAllowedEmail(t *testing.T) {
	assert.NoError(t, validateAllowedEmail("user@example.com", "example.com"))
	assert.Error(t, validateAllowedEmail("bad", "example.com"))
	assert.Error(t, validateAllowedEmail("user@other.com", "example.com"))
}

func TestIsAllowedDomainEmail(t *testing.T) {
	assert.True(t, isAllowedDomainEmail("a@Example.COM", "example.com"))
	assert.False(t, isAllowedDomainEmail("", "example.com"))
	assert.False(t, isAllowedDomainEmail("no-at-sign", "example.com"))
}

func TestRequireRoleAllowsAdmin(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/users", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleAdmin, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRequireRoleAllowsEditorForEditorRoute(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodGet, "/admin/api/keys", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleEditor, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestValidateEditorCostLimitSkipsAdmin(t *testing.T) {
	h, _ := testAdminHandler(t)
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", nil)
	err := h.validateEditorCostLimit(req, 99999999)
	assert.NoError(t, err)
}

func TestValidateCostLimitRejectsNegativeForAllRoles(t *testing.T) {
	h, _ := testAdminHandler(t)
	// Admin (the most privileged role) must still be rejected for a negative
	// limit, since a negative value would otherwise read as "unlimited".
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", nil)
	err := h.validateEditorCostLimit(req, -1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")
}

func TestValidateEditorCostLimitRejectsOverCap(t *testing.T) {
	h, _ := testAdminHandler(t)
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", nil)
	err = h.validateEditorCostLimit(req, 99999999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "editor maximum")
}

func TestErrLastAdminMessage(t *testing.T) {
	assert.Equal(t, "cannot remove the last admin", errLastAdmin.Error())
}
