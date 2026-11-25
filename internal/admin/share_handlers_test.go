package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ensureTestDynamoFake(t *testing.T) {
	t.Helper()
	if os.Getenv("AWS_ACCESS_KEY_ID") == "test" && os.Getenv("AWS_ENDPOINT_URL") != "" {
		return
	}
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
}

func testAdminUserStore(t *testing.T) *adminusers.Store {
	t.Helper()
	ensureTestDynamoFake(t)
	store, err := adminusers.NewStore(adminusers.StoreConfig{
		TableName:       "test-admin-users",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)
	return store
}

func testAdminHandler(t *testing.T) (*handler, *apikeys.Store) {
	t.Helper()
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "admin@example.com")

	ensureTestDynamoFake(t)
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true})
	require.NoError(t, err)

	userStore := testAdminUserStore(t)
	_, err = userStore.CreateUser(context.Background(), "admin@example.com", adminusers.RoleAdmin)
	require.NoError(t, err)

	yamlCfg := config.GetDefaultYAMLConfig()
	yamlCfg.Features.AdminDashboard.AllowedDomain = "example.com"
	yamlCfg.Features.AdminDashboard.DevBypassLogin = true
	yamlCfg.Features.AdminDashboard.DevCORSOrigin = "http://localhost:5173"
	yamlCfg.Features.AdminDashboard.EditorLimits.MaxDailyCostLimitCents = 5000

	auth, err := newAuthenticator(slog.Default(), yamlCfg.Features.AdminDashboard, userStore)
	require.NoError(t, err)

	h := newHandler(Deps{
		Logger:      slog.Default(),
		YAMLConfig:  yamlCfg,
		APIKeyStore: store,
		UserStore:   userStore,
	}, auth)
	return h, store
}

func authenticatedRequest(t *testing.T, h *handler, method, path string, body []byte) *http.Request {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
	}
	req.Header.Set("Content-Type", "application/json")

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	h.auth.handleDevLogin(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)
	for _, c := range loginRec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestProviderBasePath(t *testing.T) {
	assert.Equal(t, "/openai/v1", providerBasePath("openai"))
	assert.Equal(t, "/anthropic", providerBasePath("anthropic"))
	assert.Equal(t, "/gemini", providerBasePath("gemini"))
	assert.Equal(t, "/bedrock", providerBasePath("bedrock"))
	assert.Equal(t, "/openrouter/api/v1", providerBasePath("openrouter"))
}

func TestHandleCreateShare(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "demo", 0, nil, nil)
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{"key": key.PK})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", body)
	req.Host = "llm-proxy:9002"
	rec := httptest.NewRecorder()
	h.handleCreateShare(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Contains(t, resp["url"], "http://localhost:5173/share/")
	assert.Equal(t, "openai", resp["provider"])
	assert.NotEmpty(t, resp["id"])
	assert.NotNil(t, resp["expires_at"])

	// Second share within 24h returns the same link id.
	body2, _ := json.Marshal(map[string]string{"key": key.PK})
	req2 := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", body2)
	req2.Host = "llm-proxy:9002"
	rec2 := httptest.NewRecorder()
	h.handleCreateShare(rec2, req2)
	require.Equal(t, http.StatusCreated, rec2.Code)
	var resp2 map[string]any
	require.NoError(t, json.NewDecoder(rec2.Body).Decode(&resp2))
	assert.Equal(t, resp["id"], resp2["id"])
}

func TestHandleGetShare(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "anthropic", "sk-ant", "", 0, nil, nil, apikeys.KeyRateLimits{RPM: 5})
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/share/"+link.ID(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": link.ID()})
	rec := httptest.NewRecorder()
	h.handleGetShare(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "noindex, nofollow", rec.Header().Get("X-Robots-Tag"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, key.PK, resp["key"])
	assert.Equal(t, "anthropic", resp["provider"])
	assert.Equal(t, "http://localhost:9002", resp["proxy_base"])
	assert.Equal(t, "http://localhost:9002/anthropic", resp["base_url"])
	assert.NotEmpty(t, resp["expires_at"])
	assert.NotContains(t, resp, "created_by", "creator email must not be returned in share payload")
}

func TestHandleGetShare_UnauthenticatedRejected(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/share/"+link.ID(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": link.ID()})
	rec := httptest.NewRecorder()
	h.handleGetShare(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), key.PK)
	assert.NotContains(t, rec.Body.String(), "admin@example.com")
}

func TestKeyToResponse_IncludesRateLimits(t *testing.T) {
	resp := keyToResponse(&apikeys.APIKey{
		PK:           apikeys.KeyPrefix + "abc",
		Provider:     "openai",
		RateLimitRPM: 10,
		RateLimitTPM: 20,
	}, false)
	assert.Equal(t, 10, resp.RateLimitRPM)
	assert.Equal(t, 20, resp.RateLimitTPM)
}

// Signed-in org users (including viewers) can claim a valid share link even
// for keys they don't own; unauthenticated callers are rejected with 401.
func TestHandleGetShare_ViewerResolvesAnyLinkAndUnauthRejected(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	orgKey, err := store.CreateKey(ctx, "anthropic", "sk-org", "", 0, nil, nil)
	require.NoError(t, err)
	orgLink, err := store.CreateShareLink(ctx, orgKey.PK, "admin@example.com")
	require.NoError(t, err)

	orgReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/share/"+orgLink.ID(), nil)
	orgReq = mux.SetURLVars(orgReq, map[string]string{"id": orgLink.ID()})
	orgRec := httptest.NewRecorder()
	h.handleGetShare(orgRec, orgReq)
	require.Equal(t, http.StatusOK, orgRec.Code)
	var resp map[string]any
	require.NoError(t, json.NewDecoder(orgRec.Body).Decode(&resp))
	assert.Equal(t, orgKey.PK, resp["key"])
	assert.NotContains(t, resp, "created_by")

	publicReq := httptest.NewRequest(http.MethodGet, "/admin/api/share/"+orgLink.ID(), nil)
	publicReq = mux.SetURLVars(publicReq, map[string]string{"id": orgLink.ID()})
	publicRec := httptest.NewRecorder()
	h.handleGetShare(publicRec, publicReq)
	require.Equal(t, http.StatusUnauthorized, publicRec.Code)
	assert.NotContains(t, publicRec.Body.String(), orgKey.PK)
	assert.NotContains(t, publicRec.Body.String(), "admin@example.com")
}

func TestHandleGetShare_InvalidAndOversizedID(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h, store := testAdminHandler(t)

	key, err := store.CreateKey(context.Background(), "openai", "sk-real", "desc", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(context.Background(), key.PK, "admin@example.com")
	require.NoError(t, err)

	cases := []struct {
		name string
		id   string
	}{
		{"malformed", "not-a-real-id"},
		{"prefixed", apikeys.ShareKeyPrefix + link.ID()},
		{"non_hex", "11111111-2222-3333-4444-55555555555g"},
		{"oversized_3000_bytes", strings.Repeat("a", 3000)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/share/placeholder", nil)
			req = mux.SetURLVars(req, map[string]string{"id": tc.id})
			rec := httptest.NewRecorder()
			h.handleGetShare(rec, req)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}

	// Verify invalid/oversized IDs are rejected before touching DynamoDB
	// (nil UserStore avoids a session lookup GetItem on the shared fake).
	noUserAuth, err := newAuthenticator(slog.Default(), h.deps.YAMLConfig.Features.AdminDashboard, nil)
	require.NoError(t, err)
	noUserHandler := newHandler(Deps{
		Logger:      slog.Default(),
		YAMLConfig:  h.deps.YAMLConfig,
		APIKeyStore: store,
	}, noUserAuth)

	fake.FailOnce("GetItem", errors.New("ValidationException"))
	req := authenticatedRequest(t, noUserHandler, http.MethodGet, "/admin/api/share/placeholder", nil)
	req = mux.SetURLVars(req, map[string]string{"id": strings.Repeat("x", 2500)})
	rec := httptest.NewRecorder()
	noUserHandler.handleGetShare(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// Confirm the armed GetItem failure was NOT consumed by the oversized ID.
	_, err = store.GetShareLink(context.Background(), "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ValidationException")

	// Unauthenticated caller with an oversized ID still gets 401 first.
	unauthReq := httptest.NewRequest(http.MethodGet, "/admin/api/share/placeholder", nil)
	unauthReq = mux.SetURLVars(unauthReq, map[string]string{"id": strings.Repeat("a", 3000)})
	unauthRec := httptest.NewRecorder()
	h.handleGetShare(unauthRec, unauthReq)
	assert.Equal(t, http.StatusUnauthorized, unauthRec.Code)
}

func TestHandleDeleteShare_InvalidAndOversizedID(t *testing.T) {
	h, _ := testAdminHandler(t)

	for _, id := range []string{"missing", "not-a-uuid", strings.Repeat("b", 2500)} {
		req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/share/placeholder", nil)
		req = mux.SetURLVars(req, map[string]string{"id": id})
		rec := httptest.NewRecorder()
		h.handleDeleteShare(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "id len=%d body=%s", len(id), rec.Body.String())
	}
}

func TestRegisterRoutes_ShareEndpointAuthAndValidation(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h, store := testAdminHandler(t)
	ctx := context.Background()

	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)
	_, err = h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	key, err := store.CreateKey(ctx, "openai", "sk-real", "shared key", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	r := mux.NewRouter()
	RegisterRoutes(r, *h.deps)

	t.Run("unauthenticated GET returns 401 and leaks nothing", func(t *testing.T) {
		fake.FailOnce("GetItem", errors.New("should-not-hit-dynamo"))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/admin/api/share/"+link.ID(), nil)
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.NotContains(t, rec.Body.String(), key.PK)
		assert.NotContains(t, rec.Body.String(), "admin@example.com")

		// Confirm DynamoDB was not called on the unauthenticated request.
		_, err := store.GetShareLink(ctx, "00000000-0000-0000-0000-000000000000")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "should-not-hit-dynamo")
	})

	t.Run("OPTIONS preflight succeeds without session", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodOptions, "/admin/api/share/"+link.ID(), nil)
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	for _, email := range []string{"viewer@example.com", "editor@example.com", "admin@example.com"} {
		t.Run("authenticated "+email+" claims share link", func(t *testing.T) {
			req := authenticatedRequestAs(t, h, email, http.MethodGet, "/admin/api/share/"+link.ID(), nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			var resp map[string]any
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
			assert.Equal(t, key.PK, resp["key"])
			assert.Equal(t, "openai", resp["provider"])
			assert.NotContains(t, resp, "created_by")
		})
	}

	t.Run("authenticated oversized ID returns 400 not 500", func(t *testing.T) {
		oversized := strings.Repeat("a", 2500)
		req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/share/"+oversized, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	})

	t.Run("authenticated valid missing UUID returns 404", func(t *testing.T) {
		req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/share/00000000-0000-0000-0000-000000000000", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	})
}

func TestHandleCreateShare_ViewerOwnKeyOnly(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	ownKey, err := store.CreatePersonalKey(ctx, "viewer@example.com", "gemini", "sk-own", "", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	body, _ := json.Marshal(map[string]string{"key": ownKey.PK})
	ownReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/share", body)
	ownReq.Host = "llm-proxy:9002"
	ownRec := httptest.NewRecorder()
	h.handleCreateShare(ownRec, ownReq)
	require.Equal(t, http.StatusCreated, ownRec.Code)

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "", 0, nil, nil)
	require.NoError(t, err)
	orgBody, _ := json.Marshal(map[string]string{"key": orgKey.PK})
	orgReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/share", orgBody)
	orgRec := httptest.NewRecorder()
	h.handleCreateShare(orgRec, orgReq)
	assert.Equal(t, http.StatusNotFound, orgRec.Code)
}
