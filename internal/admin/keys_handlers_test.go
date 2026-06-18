package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProvisioner struct {
	key string
}

func (f *fakeProvisioner) Provision(_ context.Context, _ provision.ProvisionRequest) (provision.Result, error) {
	return provision.Result{
		ActualKey:    f.key,
		UpstreamID:   "upstream-1",
		UpstreamKind: provision.UpstreamKindOpenAIServiceAccount,
	}, nil
}

func (f *fakeProvisioner) Revoke(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeProvisioner) PoolStatus(_ context.Context) (int, bool) {
	return 1, true
}

func withTestProvisioner(t *testing.T, h *handler, providers ...string) {
	t.Helper()
	byProvider := map[string]provision.Provisioner{}
	for _, p := range providers {
		byProvider[p] = &fakeProvisioner{key: "sk-provisioned-" + p}
	}
	h.deps.KeyProvisioner = provision.NewManager(slog.Default(), byProvider)
}

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

func TestViewerPersonalKeys(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	h.deps.YAMLConfig.Features.AdminDashboard.ViewerLimits.PersonalMonthlyCostLimitCents = 1000
	withTestProvisioner(t, h, "openai")
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	listReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/keys", nil)
	listRec := httptest.NewRecorder()
	h.handleListKeys(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code)
	var listResp []KeyResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listResp))
	assert.Empty(t, listResp)

	manualBody, _ := json.Marshal(CreateKeyRequest{
		Provider:  "openai",
		ActualKey: "sk-viewer",
	})
	manualReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/keys", manualBody)
	manualRec := httptest.NewRecorder()
	h.handleCreateKey(manualRec, manualReq)
	assert.Equal(t, http.StatusBadRequest, manualRec.Code)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:      "openai",
		Description:   "mine",
		AutoProvision: true,
	})
	createReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/keys", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKey(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	var created KeyResponse
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	assert.Equal(t, int64(1000), created.MonthlyCostLimit)
	assert.Equal(t, "viewer@example.com", created.OwnerEmail)
	assert.Equal(t, int64(0), created.DailyCostLimit)

	dupReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/keys", body)
	dupRec := httptest.NewRecorder()
	h.handleCreateKey(dupRec, dupReq)
	assert.Equal(t, http.StatusConflict, dupRec.Code)

	patchReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPatch, "/admin/api/keys/"+created.Key, []byte(`{"enabled": false}`))
	patchReq = mux.SetURLVars(patchReq, map[string]string{"key": created.Key})
	patchRec := httptest.NewRecorder()
	h.handleUpdateKey(patchRec, patchReq)
	assert.Equal(t, http.StatusForbidden, patchRec.Code)

	patchDescReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPatch, "/admin/api/keys/"+created.Key, []byte(`{"description": "updated"}`))
	patchDescReq = mux.SetURLVars(patchDescReq, map[string]string{"key": created.Key})
	patchDescRec := httptest.NewRecorder()
	h.handleUpdateKey(patchDescRec, patchDescReq)
	require.Equal(t, http.StatusOK, patchDescRec.Code)

	getOrgReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/keys/"+orgKey.PK, nil)
	getOrgReq = mux.SetURLVars(getOrgReq, map[string]string{"key": orgKey.PK})
	getOrgRec := httptest.NewRecorder()
	h.handleGetKey(getOrgRec, getOrgReq)
	assert.Equal(t, http.StatusForbidden, getOrgRec.Code)

	delReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodDelete, "/admin/api/keys/"+created.Key, nil)
	delReq = mux.SetURLVars(delReq, map[string]string{"key": created.Key})
	delRec := httptest.NewRecorder()
	h.handleDeleteKey(delRec, delReq)
	assert.Equal(t, http.StatusNoContent, delRec.Code)
}

func TestEditorCreateKeyRequiresAutoProvision(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()
	withTestProvisioner(t, h, "openai")
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	manualBody, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		ActualKey:      "sk-editor",
		DailyCostLimit: 1000,
	})
	manualReq := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", manualBody)
	manualRec := httptest.NewRecorder()
	h.handleCreateKey(manualRec, manualReq)
	assert.Equal(t, http.StatusBadRequest, manualRec.Code)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		Description:    "editor key",
		DailyCostLimit: 1000,
		AutoProvision:  true,
	})
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}
