package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
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

func (f *fakeProvisioner) Rename(_ context.Context, _, _, _ string) (provision.Result, error) {
	return provision.Result{}, nil
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

// Editors are held to the Bedrock-only rule for PII-off keys; admins (below)
// may override it after confirming in the dashboard.
func TestHandleCreateKey_PIIOffRequiresBedrockForEditor(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true
	withTestProvisioner(t, h, "openai")
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		Description:    "editor key",
		DailyCostLimit: 1000,
		AutoProvision:  true,
		RedactPII:      boolPtr(false),
	})
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestHandleCreateKey_PIIOffAdminAllowed(t *testing.T) {
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
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
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

func TestHandleCreateKey_BedrockMantleWithoutActualKey(t *testing.T) {
	h, _ := testAdminHandler(t)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:    "bedrock-mantle",
		Description: "local mantle",
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestHandleCreateKey_BedrockWithoutActualKey(t *testing.T) {
	h, _ := testAdminHandler(t)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:    "bedrock",
		Description: "local bedrock",
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestHandleCreateKey_PIIOffBypassAllowlistedEditor(t *testing.T) {
	// The shipped allowlist is empty; opt this editor in for the test.
	apikeys.SetPIIOffNonBedrockBypassAdmins([]string{"editor@example.com"})
	t.Cleanup(func() { apikeys.SetPIIOffNonBedrockBypassAdmins(nil) })
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true
	withTestProvisioner(t, h, "openai")
	_, err := h.deps.UserStore.CreateUser(context.Background(), "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:       "openai",
		Description:    "editor key",
		DailyCostLimit: 1000,
		AutoProvision:  true,
		RedactPII:      boolPtr(false),
	})
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestHandleUpdateKey_PIIOffRequiresBedrockForEditor(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)

	key, err := store.CreateKey(ctx, "openai", "sk", "", 0, nil, boolPtr(true))
	require.NoError(t, err)

	body := []byte(`{"redact_pii": false}`)
	req := authenticatedRequestAs(t, h, "editor@example.com", http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestHandleUpdateKey_PIIOffAdminAllowed(t *testing.T) {
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
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated, err := store.GetKeyRecordByID(ctx, key.PK)
	require.NoError(t, err)
	require.NotNil(t, updated.RedactPII)
	assert.False(t, *updated.RedactPII)
}

func TestHandleUpdateKey_PersonalKeyRenameForbidden(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-real", "original", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	body := []byte(`{"description": "renamed"}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	var errResp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Equal(t, "personal keys cannot be renamed", errResp["error"])

	record, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Equal(t, "original", record.Description)
}

func TestHandleUpdateKey_PersonalKeyUnchangedDescriptionAllowed(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-real", "original", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	body := []byte(`{"description": "original"}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
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

func TestHandleCreateKey_MonthlyCostLimit(t *testing.T) {
	h, _ := testAdminHandler(t)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:         "openai",
		ActualKey:        "sk-real",
		Description:      "monthly cap",
		MonthlyCostLimit: 5000,
	})
	req := authenticatedRequest(t, h, http.MethodPost, "/admin/api/keys", body)
	rec := httptest.NewRecorder()
	h.handleCreateKey(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	var resp KeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, int64(0), resp.DailyCostLimit)
	assert.Equal(t, int64(5000), resp.MonthlyCostLimit)
}

func TestHandleUpdateKey_SwitchCostLimitPeriod(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "daily key", 10000, nil, nil)
	require.NoError(t, err)

	toMonthly := []byte(`{"daily_cost_limit": 0, "monthly_cost_limit": 2500}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, toMonthly)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Equal(t, int64(0), updated.DailyCostLimit)
	assert.Equal(t, int64(2500), updated.MonthlyCostLimit)

	toDaily := []byte(`{"daily_cost_limit": 8000, "monthly_cost_limit": 0}`)
	req = authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, toDaily)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec = httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	updated, err = store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Equal(t, int64(8000), updated.DailyCostLimit)
	assert.Equal(t, int64(0), updated.MonthlyCostLimit)
}

func TestHandleUpdateKey_RejectPartialCostLimitPatch(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk", "monthly key", 0, nil, nil)
	require.NoError(t, err)
	require.NoError(t, store.UpdateKey(ctx, key.PK, map[string]interface{}{
		"monthly_cost_limit": int64(3000),
	}))

	body := []byte(`{"daily_cost_limit": 10000}`)
	req := authenticatedRequest(t, h, http.MethodPatch, "/admin/api/keys/"+key.PK, body)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	rec := httptest.NewRecorder()
	h.handleUpdateKey(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	updated, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Equal(t, int64(0), updated.DailyCostLimit)
	assert.Equal(t, int64(3000), updated.MonthlyCostLimit)
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

func deleteShareAs(t *testing.T, h *handler, email, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := authenticatedRequestAs(t, h, email, http.MethodDelete, "/admin/api/share/"+id, nil)
	req = mux.SetURLVars(req, map[string]string{"id": id})
	rec := httptest.NewRecorder()
	h.handleDeleteShare(rec, req)
	return rec
}

// Revoking a share requires access to the shared key or having minted the
// link. Anything else is reported as 404, the same as a missing link.
func TestHandleDeleteShare_Authorization(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	for _, u := range []struct {
		email string
		role  adminusers.Role
	}{
		{"viewer@example.com", adminusers.RoleViewer},
		{"other-viewer@example.com", adminusers.RoleViewer},
		{"editor@example.com", adminusers.RoleEditor},
	} {
		_, err := h.deps.UserStore.CreateUser(ctx, u.email, u.role)
		require.NoError(t, err)
	}

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	t.Run("viewer cannot revoke another owner's share", func(t *testing.T) {
		link, err := store.CreateShareLink(ctx, orgKey.PK, "admin@example.com")
		require.NoError(t, err)

		rec := deleteShareAs(t, h, "viewer@example.com", link.ID())
		assert.Equal(t, http.StatusNotFound, rec.Code)
		missing := deleteShareAs(t, h, "viewer@example.com", "00000000-0000-0000-0000-000000000000")
		assert.Equal(t, missing.Body.String(), rec.Body.String())

		_, err = store.GetShareLink(ctx, link.ID())
		require.NoError(t, err, "link must survive the viewer's attempt")
	})

	t.Run("creator revokes own share of a key they cannot access", func(t *testing.T) {
		link, err := store.CreateShareLink(ctx, orgKey.PK, "admin@example.com")
		require.NoError(t, err)
		require.NoError(t, store.DeleteShareLink(ctx, link.ID()))
		// Mint a link attributed to the viewer, as if an earlier policy allowed it.
		link, err = store.CreateShareLink(ctx, orgKey.PK, "viewer@example.com")
		require.NoError(t, err)

		other := deleteShareAs(t, h, "other-viewer@example.com", link.ID())
		assert.Equal(t, http.StatusNotFound, other.Code)
		rec := deleteShareAs(t, h, "viewer@example.com", link.ID())
		assert.Equal(t, http.StatusNoContent, rec.Code)
		_, err = store.GetShareLink(ctx, link.ID())
		require.Error(t, err)
	})

	t.Run("editor revokes any share", func(t *testing.T) {
		link, err := store.CreateShareLink(ctx, orgKey.PK, "admin@example.com")
		require.NoError(t, err)

		rec := deleteShareAs(t, h, "editor@example.com", link.ID())
		assert.Equal(t, http.StatusNoContent, rec.Code)
		_, err = store.GetShareLink(ctx, link.ID())
		require.Error(t, err)
	})

	t.Run("viewer revokes share of own personal key", func(t *testing.T) {
		personal, err := store.CreatePersonalKey(ctx, "viewer@example.com", "gemini", "sk-own", "", 1000, apikeys.KeyCreateMeta{})
		require.NoError(t, err)
		link, err := store.CreateShareLink(ctx, personal.PK, "admin@example.com")
		require.NoError(t, err)

		rec := deleteShareAs(t, h, "viewer@example.com", link.ID())
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
}

func TestHandleDeleteShare_ExpiredLinkStillRevocable(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	personal, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-own", "", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	expiredID := "11111111-2222-3333-4444-555555555555"
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	fake.InjectItem("test-keys", apikeys.ShareKeyPrefix+expiredID, map[string]any{
		"pk":               map[string]any{"S": apikeys.ShareKeyPrefix + expiredID},
		"share_api_key":    map[string]any{"S": personal.PK},
		"share_provider":   map[string]any{"S": "openai"},
		"share_created_by": map[string]any{"S": "admin@example.com"},
		"created_at":       map[string]any{"S": time.Now().Add(-25 * time.Hour).Format(time.RFC3339Nano)},
		"expires_at":       map[string]any{"S": past},
	})
	_, err = store.GetShareLink(ctx, expiredID)
	require.Error(t, err, "precondition: link is expired")

	rec := deleteShareAs(t, h, "viewer@example.com", expiredID)
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	_, err = store.GetShareLinkRecord(ctx, expiredID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHandleDeleteShare_NotFound(t *testing.T) {
	h, _ := testAdminHandler(t)

	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/share/missing", nil)
	req = mux.SetURLVars(req, map[string]string{"id": "missing"})
	rec := httptest.NewRecorder()
	h.handleDeleteShare(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// A viewer must not be able to tell "exists but not mine" from "does not
// exist": every key object route answers both with the same 404 body.
func TestKeyRoutes_InaccessibleKeyIndistinguishableFromMissing(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)
	missingKey := apikeys.KeyPrefix + "0000000000000000000000000000000000000000000000000000000000000000"

	routes := []struct {
		name string
		call func(key string) *httptest.ResponseRecorder
	}{
		{"get", func(key string) *httptest.ResponseRecorder {
			req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/keys/"+key, nil)
			req = mux.SetURLVars(req, map[string]string{"key": key})
			rec := httptest.NewRecorder()
			h.handleGetKey(rec, req)
			return rec
		}},
		{"update", func(key string) *httptest.ResponseRecorder {
			req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPatch, "/admin/api/keys/"+key, []byte(`{"description":"x"}`))
			req = mux.SetURLVars(req, map[string]string{"key": key})
			rec := httptest.NewRecorder()
			h.handleUpdateKey(rec, req)
			return rec
		}},
		{"delete", func(key string) *httptest.ResponseRecorder {
			req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodDelete, "/admin/api/keys/"+key, nil)
			req = mux.SetURLVars(req, map[string]string{"key": key})
			rec := httptest.NewRecorder()
			h.handleDeleteKey(rec, req)
			return rec
		}},
		{"stats", func(key string) *httptest.ResponseRecorder {
			req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/keys/"+key+"/stats", nil)
			req = mux.SetURLVars(req, map[string]string{"key": key})
			rec := httptest.NewRecorder()
			h.handleKeyStats(rec, req)
			return rec
		}},
		{"share", func(key string) *httptest.ResponseRecorder {
			body, _ := json.Marshal(map[string]string{"key": key})
			req := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/share", body)
			rec := httptest.NewRecorder()
			h.handleCreateShare(rec, req)
			return rec
		}},
	}

	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			other := rt.call(orgKey.PK)
			missing := rt.call(missingKey)
			assert.Equal(t, http.StatusNotFound, other.Code, "other owner's key: %s", other.Body.String())
			assert.Equal(t, http.StatusNotFound, missing.Code, "missing key: %s", missing.Body.String())
			assert.Equal(t, missing.Body.String(), other.Body.String())
		})
	}

	// The org key is untouched by the viewer's delete attempt.
	_, err = store.GetKeyRecord(ctx, orgKey.PK)
	require.NoError(t, err)
}

// Caller mistakes stay 400; backend failures are logged and become a generic
// 500 so wrapped DynamoDB errors never reach the client.
func TestKeyRoutes_LookupErrorClasses(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	h, store := testAdminHandler(t)
	key, err := store.CreateKey(context.Background(), "openai", "sk", "", 0, nil, nil)
	require.NoError(t, err)

	// Masked ids resolve via Scan, which the session lookup never issues, so a
	// one-shot Scan failure armed after the session exists hits only the key
	// lookup.
	masked := apikeys.MaskKeyID(key.PK)
	getKey := func(id string, storeErr error) *httptest.ResponseRecorder {
		req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys/"+id, nil)
		req = mux.SetURLVars(req, map[string]string{"key": id})
		if storeErr != nil {
			fake.FailOnce("Scan", storeErr)
		}
		rec := httptest.NewRecorder()
		h.handleGetKey(rec, req)
		return rec
	}

	rec := getKey("not-a-key-id", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = getKey(masked, errors.New("InternalServerError"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error":"failed to load key"}`, rec.Body.String())

	rec = getKey(masked, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
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
	assert.Equal(t, http.StatusForbidden, patchDescRec.Code)

	getOrgReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodGet, "/admin/api/keys/"+orgKey.PK, nil)
	getOrgReq = mux.SetURLVars(getOrgReq, map[string]string{"key": orgKey.PK})
	getOrgRec := httptest.NewRecorder()
	h.handleGetKey(getOrgRec, getOrgReq)
	assert.Equal(t, http.StatusNotFound, getOrgRec.Code)

	delReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodDelete, "/admin/api/keys/"+created.Key, nil)
	delReq = mux.SetURLVars(delReq, map[string]string{"key": created.Key})
	delRec := httptest.NewRecorder()
	h.handleDeleteKey(delRec, delReq)
	assert.Equal(t, http.StatusNoContent, delRec.Code)
}

func TestAdminCreatePersonalKey(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.AdminDashboard.ViewerLimits.PersonalMonthlyCostLimitCents = 1000
	withTestProvisioner(t, h, "openai")

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:      "openai",
		Description:   "my personal key",
		AutoProvision: true,
		Personal:      true,
	})
	createReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/keys", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKey(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	var created KeyResponse
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	assert.Equal(t, int64(1000), created.MonthlyCostLimit)
	assert.Equal(t, "admin@example.com", created.OwnerEmail)
	assert.Equal(t, int64(0), created.DailyCostLimit)

	dupReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/keys", body)
	dupRec := httptest.NewRecorder()
	h.handleCreateKey(dupRec, dupReq)
	assert.Equal(t, http.StatusConflict, dupRec.Code)
}

func TestAdminCreatePersonalBedrockKey(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig.Features.AdminDashboard.ViewerLimits.PersonalMonthlyCostLimitCents = 1000

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:    "bedrock",
		Description: "my bedrock personal key",
		Personal:    true,
	})
	createReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/keys", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKey(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	var created KeyResponse
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	assert.Equal(t, "bedrock", created.Provider)
	assert.Equal(t, int64(1000), created.MonthlyCostLimit)
	assert.Equal(t, "admin@example.com", created.OwnerEmail)
	assert.Equal(t, int64(0), created.DailyCostLimit)

	// auto_provision is ignored for AWS-auth providers (no upstream secret).
	bodyAuto, _ := json.Marshal(CreateKeyRequest{
		Provider:      "bedrock",
		Description:   "dup with auto_provision",
		AutoProvision: true,
		Personal:      true,
	})
	dupReq := authenticatedRequestAs(t, h, "admin@example.com", http.MethodPost, "/admin/api/keys", bodyAuto)
	dupRec := httptest.NewRecorder()
	h.handleCreateKey(dupRec, dupReq)
	assert.Equal(t, http.StatusConflict, dupRec.Code)
}

func TestViewerCreatePersonalBedrockKey(t *testing.T) {
	h, _ := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	body, _ := json.Marshal(CreateKeyRequest{
		Provider:    "bedrock",
		Description: "viewer bedrock",
	})
	createReq := authenticatedRequestAs(t, h, "viewer@example.com", http.MethodPost, "/admin/api/keys", body)
	createRec := httptest.NewRecorder()
	h.handleCreateKey(createRec, createReq)
	require.Equal(t, http.StatusCreated, createRec.Code, createRec.Body.String())

	var created KeyResponse
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	assert.Equal(t, "bedrock", created.Provider)
	assert.Equal(t, "viewer@example.com", created.OwnerEmail)
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
