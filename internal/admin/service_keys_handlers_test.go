package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listServiceKeysAs(t *testing.T, h *handler, email string) *httptest.ResponseRecorder {
	t.Helper()
	req := authenticatedRequestAs(t, h, email, http.MethodGet, "/admin/api/service-keys", nil)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleViewer, h.handleListServiceKeys).ServeHTTP(rec, req)
	return rec
}

func TestHandleListServiceKeys(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	for _, u := range []struct {
		email string
		role  adminusers.Role
	}{
		{"grantee@example.com", adminusers.RoleViewer},
		{"plain@example.com", adminusers.RoleViewer},
		{"editor@example.com", adminusers.RoleEditor},
	} {
		_, err := h.deps.UserStore.CreateUser(ctx, u.email, u.role)
		require.NoError(t, err)
	}
	require.NoError(t, h.deps.UserStore.SetServiceKeyAccess(ctx, "grantee@example.com", true))

	orgKey, err := store.CreateKeyWithMeta(ctx, "openai", "sk-org", "shared service key", 500, 0, nil, nil,
		apikeys.KeyCreateMeta{RequesterEmail: "requester@example.com"})
	require.NoError(t, err)
	_, err = store.CreatePersonalKey(ctx, "plain@example.com", "openai", "sk-own", "mine", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	t.Run("granted viewer sees only service keys, without the credential", func(t *testing.T) {
		rec := listServiceKeysAs(t, h, "grantee@example.com")
		require.Equal(t, http.StatusOK, rec.Code)

		var keys []KeyResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &keys))
		require.Len(t, keys, 1)
		assert.Equal(t, "shared service key", keys[0].Description)
		assert.Equal(t, apikeys.MaskKeyID(orgKey.PK), keys[0].Key)
		assert.Empty(t, keys[0].ActualKey)
		assert.Empty(t, keys[0].OwnerEmail)
		assert.Empty(t, keys[0].RequesterEmail)
		assert.NotContains(t, rec.Body.String(), orgKey.PK)
		assert.NotContains(t, rec.Body.String(), "sk-org")
	})

	t.Run("ungranted viewer is forbidden", func(t *testing.T) {
		rec := listServiceKeysAs(t, h, "plain@example.com")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("editor may list", func(t *testing.T) {
		rec := listServiceKeysAs(t, h, "editor@example.com")
		require.Equal(t, http.StatusOK, rec.Code)
		var keys []KeyResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &keys))
		assert.Len(t, keys, 1)
	})

	t.Run("revoking the grant takes effect immediately", func(t *testing.T) {
		require.NoError(t, h.deps.UserStore.SetServiceKeyAccess(ctx, "grantee@example.com", false))
		rec := listServiceKeysAs(t, h, "grantee@example.com")
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestHandleListServiceKeysUnauthenticated(t *testing.T) {
	h, _ := testAdminHandler(t)
	rec := httptest.NewRecorder()
	roleHandler(h.auth, adminusers.RoleViewer, h.handleListServiceKeys).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/service-keys", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHandleListServiceKeysStoreUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.APIKeyStore = nil
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/service-keys", nil)
	rec := httptest.NewRecorder()
	h.handleListServiceKeys(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// The grant is read-only visibility of the list: a granted viewer still gets
// the same 404 as before on every per-key route for a service key, and their
// own /keys list stays scoped to personal keys.
func TestServiceKeyGrantDoesNotWidenKeyRoutes(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "grantee@example.com", adminusers.RoleViewer)
	require.NoError(t, err)
	require.NoError(t, h.deps.UserStore.SetServiceKeyAccess(ctx, "grantee@example.com", true))

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	req := authenticatedRequestAs(t, h, "grantee@example.com", http.MethodGet, "/admin/api/keys/"+orgKey.PK, nil)
	req = mux.SetURLVars(req, map[string]string{"key": orgKey.PK})
	rec := httptest.NewRecorder()
	h.handleGetKey(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	req = authenticatedRequestAs(t, h, "grantee@example.com", http.MethodDelete, "/admin/api/keys/"+orgKey.PK, nil)
	req = mux.SetURLVars(req, map[string]string{"key": orgKey.PK})
	rec = httptest.NewRecorder()
	h.handleDeleteKey(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	req = authenticatedRequestAs(t, h, "grantee@example.com", http.MethodGet, "/admin/api/keys", nil)
	rec = httptest.NewRecorder()
	h.handleListKeys(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var keys []KeyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &keys))
	assert.Empty(t, keys)
}

func TestHandleMeReportsServiceKeyVisibility(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()
	for _, email := range []string{"grantee@example.com", "plain@example.com"} {
		_, err := h.deps.UserStore.CreateUser(ctx, email, adminusers.RoleViewer)
		require.NoError(t, err)
	}
	require.NoError(t, h.deps.UserStore.SetServiceKeyAccess(ctx, "grantee@example.com", true))

	me := func(email string) UserResponse {
		req := authenticatedRequestAs(t, h, email, http.MethodGet, "/admin/api/me", nil)
		rec := httptest.NewRecorder()
		h.handleMe(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		var u UserResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &u))
		return u
	}
	assert.True(t, me("grantee@example.com").CanViewServiceKeys)
	assert.False(t, me("plain@example.com").CanViewServiceKeys)
	assert.True(t, me("admin@example.com").CanViewServiceKeys)
}
