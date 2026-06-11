package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(v bool) *bool { return &v }

func TestHandleCreateKey_PIIOffRequiresBedrock(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-real",
		RedactPII: boolPtr(false),
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateKey_PIIOffBedrockAllowed(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:  "bedrock",
		ActualKey: "unused",
		RedactPII: boolPtr(false),
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestHandleCreateKey_PIIOffBypassAdmin(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "admin@example.com")
	// The shipped allowlist is empty; opt this admin in for the test.
	apikeys.SetPIIOffNonBedrockBypassAdmins([]string{"admin@example.com"})
	t.Cleanup(func() { apikeys.SetPIIOffNonBedrockBypassAdmins(nil) })
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-real",
		RedactPII: boolPtr(false),
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestHandleUpdateKey_PIIOffRequiresBedrock(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, boolPtr(true))
	require.NoError(t, err)

	body := []byte(`{"redact_pii": false}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleCreateKey_WithRateLimits(t *testing.T) {
	h, _ := testAdminHandler(t)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:     "openai",
		ActualKey:    "sk-real",
		Description:  "limited",
		RateLimitRPM: 11,
		RateLimitTPM: 22,
		RateLimitRPD: 33,
		RateLimitTPD: 44,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp KeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, 11, resp.RateLimitRPM)
	assert.Equal(t, 22, resp.RateLimitTPM)
	assert.Equal(t, 33, resp.RateLimitRPD)
	assert.Equal(t, 44, resp.RateLimitTPD)
}

func TestHandleUpdateKey_RateLimits(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)

	body := []byte(`{"rate_limit_rpm":9}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestHandleCreateShare_Errors(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	t.Run("invalid json", func(t *testing.T) {
		req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", []byte("{"))
		rec := httptest.NewRecorder()
		h.handleCreateShare(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("missing key", func(t *testing.T) {
		req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", []byte(`{}`))
		rec := httptest.NewRecorder()
		h.handleCreateShare(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("unknown key", func(t *testing.T) {
		body, _ := json.Marshal(map[string]string{"key": apikeys.KeyPrefix + "missing"})
		req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", body)
		rec := httptest.NewRecorder()
		h.handleCreateShare(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("nil store", func(t *testing.T) {
		nilHandler := newHandler(Deps{Logger: h.deps.Logger, YAMLConfig: h.deps.YAMLConfig}, h.auth)
		req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/share", []byte(`{"key":"x"}`))
		rec := httptest.NewRecorder()
		nilHandler.handleCreateShare(rec, req)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})

	_ = store
	_ = ctx
}

func TestHandleGetShare_NotFound(t *testing.T) {
	h, _ := testAdminHandler(t)

	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/share/not-a-real-id", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "not-a-real-id"})
	rec := httptest.NewRecorder()
	h.handleGetShare(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleGetShare_KeyDeleted(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "")
	require.NoError(t, err)
	require.NoError(t, store.DeleteKey(ctx, key.PK))

	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/share/"+link.ID(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": link.ID()})
	rec := httptest.NewRecorder()
	h.handleGetShare(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleDeleteShare(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "")
	require.NoError(t, err)

	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/share/"+link.ID(), nil)
	req = mux.SetURLVars(req, map[string]string{"id": link.ID()})
	rec := httptest.NewRecorder()
	h.handleDeleteShare(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	_, err = store.GetShareLink(ctx, link.ID())
	require.Error(t, err)
}

func TestHandleDeleteShare_NotFound(t *testing.T) {
	h, _ := testAdminHandler(t)

	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/share/missing", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "missing"})
	rec := httptest.NewRecorder()
	h.handleDeleteShare(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestPublicBaseURL_YAMLOverride(t *testing.T) {
	h := &handler{deps: &Deps{YAMLConfig: &config.YAMLConfig{
		Features: config.FeaturesConfig{
			AdminDashboard: config.AdminDashboardConfig{
				PublicBaseURL: "https://llm.example.com",
			},
		},
	}}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "https://llm.example.com", h.publicBaseURL(req))
}
