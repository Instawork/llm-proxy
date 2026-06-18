package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/pii"
	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testAdminRollupStore(t *testing.T) *adminrollup.Store {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis: &config.RedisConfig{
			Address: mr.Addr(),
			DB:      6,
			DBSet:   true,
		},
		HistoryDays: 7,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func keyStatsRequest(t *testing.T, h *handler, email, key string) (*httptest.ResponseRecorder, keyStatsResponse) {
	t.Helper()
	req := authenticatedRequestAs(t, h, email, http.MethodGet, "/admin/api/keys/"+key+"/stats", nil)
	req = mux.SetURLVars(req, map[string]string{"key": key})
	rec := httptest.NewRecorder()
	h.handleKeyStats(rec, req)
	var body keyStatsResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	}
	return rec, body
}

func TestHandleKeyStats_RedisDirectRead(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")

	created, err := store.CreateKey(context.Background(), "openai", "upstream", "stats key", 0, nil, nil, apikeys.KeyRateLimits{})
	require.NoError(t, err)

	rollup := testAdminRollupStore(t)
	masked := middleware.MaskKeyID(created.PK)
	require.NoError(t, rollup.ApplyDelta(ctx, adminrollup.MetricCost, day, adminrollup.Delta{
		Totals: map[string]float64{"spend_usd": 0.42},
		Dimensions: map[string]map[string]float64{
			"by_key": {adminrollup.DimMemberField(masked, "spend_usd"): 0.42},
		},
	}))

	costRec := coststats.NewRecorder()
	h.deps.AdminRollupStore = rollup
	h.deps.CostSummary = costRec.Snapshot
	h.deps.PIISummary = pii.NewRecorder().Snapshot

	rec, body := keyStatsRequest(t, h, "admin@example.com", created.PK)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, body.RollupAvailable)
	require.Equal(t, masked, body.MaskedKeyID)
	require.InDelta(t, 0.42, body.CostToday.SpendUSD, 1e-9)
	require.Equal(t, "redis", body.CostToday.Source)
}

func TestHandleKeyStats_ForbiddenForOtherUsersKey(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	orgKey, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	rec, _ := keyStatsRequest(t, h, "viewer@example.com", orgKey.PK)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandleKeyStats_ViewerOwnKey(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)

	personal, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-viewer", "mine", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	costRec := coststats.NewRecorder()
	masked := middleware.MaskKeyID(personal.PK)
	costRec.RecordRequest("openai", masked, "secret-user", "gpt-4o", 0.02, 0.01, 0.01, 20, 10)
	h.deps.CostSummary = costRec.Snapshot
	h.deps.PIISummary = pii.NewRecorder().Snapshot

	rec, body := keyStatsRequest(t, h, "viewer@example.com", personal.PK)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, masked, body.MaskedKeyID)
	require.InDelta(t, 0.02, body.CostToday.SpendUSD, 1e-9)
	require.Len(t, body.RecentCost, 1)
}

func TestHandleKeyStats_NotFoundForMissingKey(t *testing.T) {
	h, _ := testAdminHandler(t)
	rec, _ := keyStatsRequest(t, h, "admin@example.com", apikeys.KeyPrefix+"missing")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleKeyStats_StripsUserIDFromRecentCost(t *testing.T) {
	h, store := testAdminHandler(t)
	created, err := store.CreateKey(context.Background(), "openai", "upstream", "stats key", 0, nil, nil, apikeys.KeyRateLimits{})
	require.NoError(t, err)

	masked := middleware.MaskKeyID(created.PK)
	costRec := coststats.NewRecorder()
	costRec.RecordRequest("openai", masked, "secret-user", "gpt-4o", 0.01, 0.005, 0.005, 10, 5)
	h.deps.CostSummary = costRec.Snapshot
	h.deps.PIISummary = pii.NewRecorder().Snapshot

	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/keys/"+created.PK+"/stats", nil)
	req = mux.SetURLVars(req, map[string]string{"key": created.PK})
	rec := httptest.NewRecorder()
	h.handleKeyStats(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	rawBody := rec.Body.String()
	assert.NotContains(t, rawBody, "secret-user")
	assert.NotContains(t, rawBody, "user_id")

	var body keyStatsResponse
	require.NoError(t, json.Unmarshal([]byte(rawBody), &body))
	require.Len(t, body.RecentCost, 1)
	require.InDelta(t, 0.01, body.RecentCost[0].SpendUSD, 1e-9)
}

func TestHandleKeyStats_StoreUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.APIKeyStore = nil
	rec := httptest.NewRecorder()
	req := authenticatedRequest(t, h, http.MethodGet, "/admin/api/keys/"+apikeys.KeyPrefix+"x/stats", nil)
	req = mux.SetURLVars(req, map[string]string{"key": apikeys.KeyPrefix + "x"})
	h.handleKeyStats(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
