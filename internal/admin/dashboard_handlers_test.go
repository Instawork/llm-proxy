package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/pii"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/Instawork/llm-proxy/internal/usagestats"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDashboardHandler(t *testing.T) (*handler, *apikeys.Store) {
	t.Helper()
	h, store := testAdminHandler(t)

	costRec := coststats.NewRecorder()
	usageRec := usagestats.NewRecorder()
	piiRec := pii.NewRecorder()
	costRec.RecordRequest("openai", "iw:abc1234", "user-1", "gpt-4", 0.01, 0.005, 0.005, 100, 50)
	usageRec.RecordRequest("openai", "gpt-4", "iw:abc1234", "user-1", 100, 50)
	piiRec.RecordRedaction("openai", "iw:secret-key", nil, 0, time.Millisecond, pii.OutcomeOK)

	h.deps.YAMLConfig.Features.PIIRedact.Enabled = true
	h.deps.CostSummary = costRec.Snapshot
	h.deps.UsageSummary = usageRec.Snapshot
	h.deps.PIISummary = piiRec.Snapshot
	h.deps.RateLimiter = ratelimit.NewMemoryLimiter(h.deps.YAMLConfig)
	h.deps.HealthFunc = func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	return h, store
}

func decodeJSONBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var out map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	return out
}

func TestHandleCost_WithStats(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleCost(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/cost", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	assert.Equal(t, true, body["enabled"])
	stats, ok := body["stats"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, stats["available"])
	assert.Greater(t, stats["requests_today"], float64(0))
}

func TestHandleCost_StatsUnavailableWithoutRecorder(t *testing.T) {
	h, _ := testAdminHandler(t)
	rec := httptest.NewRecorder()
	h.handleCost(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/cost", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	stats := body["stats"].(map[string]interface{})
	assert.Equal(t, false, stats["available"])
}

func TestHandleUsageAndPII_StatsUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	for _, fn := range []func(http.ResponseWriter, *http.Request){
		h.handleUsage,
		h.handlePII,
	} {
		rec := httptest.NewRecorder()
		fn(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/stats", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		stats := decodeJSONBody(t, rec)["stats"].(map[string]interface{})
		assert.Equal(t, false, stats["available"])
	}
}

func TestHandleListKeys_StoreUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.APIKeyStore = nil
	rec := httptest.NewRecorder()
	h.handleListKeys(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleConfig_Unavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig = nil
	rec := httptest.NewRecorder()
	h.handleConfig(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/config", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleCost_ConfigUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.YAMLConfig = nil
	rec := httptest.NewRecorder()
	h.handleCost(rec, httptest.NewRequest(http.MethodGet, "/admin/api/cost", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleUsage_WithStats(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleUsage(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/usage", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	stats := body["stats"].(map[string]interface{})
	assert.Equal(t, true, stats["available"])
	assert.Equal(t, "cost_tracking", body["source"])
}

func TestHandlePII_WithStats(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handlePII(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/pii", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	stats := body["stats"].(map[string]interface{})
	assert.Equal(t, true, stats["available"])
	assert.Equal(t, true, body["enabled"])
}

func TestHandleConfig(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleConfig(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/config", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	features, ok := body["features"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, features, "rate_limiting")
	providers, ok := body["providers"].(map[string]interface{})
	require.True(t, ok)
	assert.NotEmpty(t, providers)
}

func TestHandleMe_Authenticated(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleMe(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/me", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	assert.NotEmpty(t, body["email"])
}

func TestHandleMe_Unauthorized(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleMe(rec, httptest.NewRequest(http.MethodGet, "/admin/api/me", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHandleHealth(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	h.handleHealth(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/health", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "ok")
}

func TestHandleHealth_Unavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	rec := httptest.NewRecorder()
	h.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/admin/api/health", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleRateLimits_WithSnapshotAndRedactedOverrides(t *testing.T) {
	h, _ := testDashboardHandler(t)
	h.deps.YAMLConfig.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{
		"iw:super-secret-key-12345": {RequestsPerMinute: 10},
	}
	h.deps.YAMLConfig.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"user@example.com": {TokensPerMinute: 5000},
	}

	rec := httptest.NewRecorder()
	h.handleRateLimits(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/rate-limits", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp RateLimitsResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "memory", resp.Backend)
	require.NotNil(t, resp.Snapshot)
	require.Contains(t, resp.Overrides.PerKey, "key:••••2345")
	require.Contains(t, resp.Overrides.PerUser, "user:••••.com")
}

func TestHandleListKeys(t *testing.T) {
	h, store := testDashboardHandler(t)
	_, err := store.CreateKey(context.Background(), "openai", "sk-test", "desc", 0, nil, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.handleListKeys(rec, authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var keys []map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&keys))
	require.Len(t, keys, 1)
	assert.Equal(t, "openai", keys[0]["provider"])
}

func TestHandleGetKey(t *testing.T) {
	h, store := testDashboardHandler(t)
	key, err := store.CreateKey(context.Background(), "anthropic", "sk-ant", "k1", 0, nil, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys/"+key.PK, nil)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	h.handleGetKey(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeJSONBody(t, rec)
	assert.Equal(t, key.PK, body["key"])
	assert.Equal(t, maskedActualKey, body["actual_key"])
}

func TestHandleGetKey_NotFound(t *testing.T) {
	h, _ := testDashboardHandler(t)
	rec := httptest.NewRecorder()
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys/missing", nil)
	missingKey := apikeys.KeyPrefix + "0000000000000000000000000000000000000000000000000000000000000000"
	req = mux.SetURLVars(req, map[string]string{"key": missingKey})
	h.handleGetKey(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleDeleteKey(t *testing.T) {
	h, store := testDashboardHandler(t)
	key, err := store.CreateKey(context.Background(), "openai", "sk-del", "del", 0, nil, nil)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := authenticatedRequest(t, h, http.MethodDelete, "/admin/api/keys/"+key.PK, nil)
	req = mux.SetURLVars(req, map[string]string{"key": key.PK})
	h.handleDeleteKey(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	_, err = store.GetKeyRecord(context.Background(), key.PK)
	require.Error(t, err)
}

func TestCorsMiddleware_OptionsPreflight(t *testing.T) {
	h, _ := testDashboardHandler(t)
	h.deps.YAMLConfig.Features.AdminDashboard.DevCORSOrigin = "http://localhost:5173"

	nextCalled := false
	mw := h.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/admin/api/me", nil)
	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, nextCalled)
	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestRegisterRoutes_ServesDashboardAPI(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")

	costRec := coststats.NewRecorder()
	usageRec := usagestats.NewRecorder()
	piiRec := pii.NewRecorder()

	yamlCfg := config.GetDefaultYAMLConfig()
	yamlCfg.Features.AdminDashboard.DevBypassLogin = true

	r := mux.NewRouter()
	RegisterRoutes(r, Deps{
		Logger:       testLogger(),
		YAMLConfig:   yamlCfg,
		CostSummary:  costRec.Snapshot,
		UsageSummary: usageRec.Snapshot,
		PIISummary:   piiRec.Snapshot,
		HealthFunc: func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		},
	})

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	costHTTPRec := httptest.NewRecorder()
	costReq := httptest.NewRequest(http.MethodGet, "/admin/api/cost", nil)
	for _, c := range loginRec.Result().Cookies() {
		costReq.AddCookie(c)
	}
	r.ServeHTTP(costHTTPRec, costReq)
	require.Equal(t, http.StatusOK, costHTTPRec.Code)
}

func TestRegisterRoutes_RedirectsBareAdminToSlash(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")

	yamlCfg := config.GetDefaultYAMLConfig()
	yamlCfg.Features.AdminDashboard.DevBypassLogin = true

	r := mux.NewRouter()
	RegisterRoutes(r, Deps{
		Logger:     testLogger(),
		YAMLConfig: yamlCfg,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusMovedPermanently, rec.Code)
	require.Equal(t, "/admin/", rec.Header().Get("Location"))
}
