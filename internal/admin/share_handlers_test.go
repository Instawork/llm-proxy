package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2"})
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
	assert.Contains(t, resp["url"], "http://localhost:5173/admin/share/")
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

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, key.PK, resp["key"])
	assert.Equal(t, "anthropic", resp["provider"])
	assert.Equal(t, "http://localhost:9002", resp["proxy_base"])
	assert.Equal(t, "http://localhost:9002/anthropic", resp["base_url"])
	assert.NotEmpty(t, resp["expires_at"])
}

func TestHandleGetShare_PublicAccess(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/share/"+link.ID(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": link.ID()})
	rec := httptest.NewRecorder()
	h.handleGetShare(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, key.PK, resp["key"])
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

func TestHandleGetShare_ViewerScoped(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	ownKey, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-own", "", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)
	ownLink, err := store.CreateShareLink(ctx, ownKey.PK, "viewer@example.com")
	require.NoError(t, err)

	ownReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/share/"+ownLink.ID(), nil)
	ownReq = mux.SetURLVars(ownReq, map[string]string{"id": ownLink.ID()})
	ownRec := httptest.NewRecorder()
	h.handleGetShare(ownRec, ownReq)
	require.Equal(t, http.StatusOK, ownRec.Code)

	orgKey, err := store.CreateKey(ctx, "anthropic", "sk-org", "", 0, nil, nil)
	require.NoError(t, err)
	orgLink, err := store.CreateShareLink(ctx, orgKey.PK, "admin@example.com")
	require.NoError(t, err)

	orgReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/share/"+orgLink.ID(), nil)
	orgReq = mux.SetURLVars(orgReq, map[string]string{"id": orgLink.ID()})
	orgRec := httptest.NewRecorder()
	h.handleGetShare(orgRec, orgReq)
	assert.Equal(t, http.StatusForbidden, orgRec.Code)

	publicReq := httptest.NewRequest(http.MethodGet, "/admin/api/share/"+orgLink.ID(), nil)
	publicReq = mux.SetURLVars(publicReq, map[string]string{"id": orgLink.ID()})
	publicRec := httptest.NewRecorder()
	h.handleGetShare(publicRec, publicReq)
	require.Equal(t, http.StatusOK, publicRec.Code)
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
	assert.Equal(t, http.StatusForbidden, orgRec.Code)
}
