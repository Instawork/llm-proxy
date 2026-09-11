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
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/unmeteredstats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spendTestKey(pk, provider, owner string) *apikeys.APIKey {
	return &apikeys.APIKey{PK: pk, Provider: provider, OwnerEmail: owner, Enabled: true, Description: provider + " key"}
}

func TestBuildSpendOverview_MineScopesToOwnedKeysAndDerivesProviders(t *testing.T) {
	mine1 := spendTestKey("iw:mine-openai", "openai", "viewer@example.com")
	mine2 := spendTestKey("iw:mine-anthropic", "anthropic", "viewer@example.com")
	foreign := spendTestKey("iw:other", "openai", "other@example.com")

	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", middleware.MaskKeyID(mine1.PK), "", "gpt", 1.0, 0.6, 0.4, 100, 50)
	rec.RecordRequest("anthropic", middleware.MaskKeyID(mine2.PK), "", "claude", 2.0, 1.0, 1.0, 200, 100)
	rec.RecordRequest("openai", middleware.MaskKeyID(foreign.PK), "", "gpt", 50.0, 25, 25, 1, 1)

	resp := buildSpendOverview(spendInputs{
		scope:       spendScopeMine,
		role:        adminusers.RoleViewer,
		email:       "viewer@example.com",
		day:         "2026-09-11",
		month:       "2026-09",
		keys:        []*apikeys.APIKey{mine1, mine2, foreign},
		snap:        rec.Snapshot(),
		rollupOK:    true,
		monthByKey:  map[string]float64{middleware.MaskKeyID(mine2.PK): 7.5},
		personalCap: 2000,
	})

	require.Len(t, resp.Keys, 2, "foreign key must be dropped even if the lister returned it")
	assert.Equal(t, middleware.MaskKeyID(mine2.PK), resp.Keys[0].KeyID, "sorted by month spend desc")
	assert.InDelta(t, 7.5, resp.Keys[0].Month.SpendUSD, 1e-9)
	assert.InDelta(t, 1.0, resp.Keys[1].Month.SpendUSD, 1e-9, "month never less than today")
	assert.Equal(t, "redislive", resp.Keys[1].Month.Source)
	for _, row := range resp.Keys {
		require.NotNil(t, row.Cap)
		assert.Equal(t, spendCapResponse{Period: "monthly", Cents: 2000}, *row.Cap)
		assert.Empty(t, row.OwnerEmail, "owner is implicit in mine scope")
	}

	require.Len(t, resp.Providers, 2)
	assert.Equal(t, "anthropic", resp.Providers[0].Name)
	assert.InDelta(t, 2.0, resp.Providers[0].Today.SpendUSD, 1e-9)
	assert.InDelta(t, 7.5, resp.Providers[0].Month.SpendUSD, 1e-9)
	assert.Equal(t, "openai", resp.Providers[1].Name)

	assert.InDelta(t, 3.0, resp.Totals.Today.SpendUSD, 1e-9)
	assert.Equal(t, int64(2), resp.Totals.Today.Requests)
	assert.InDelta(t, 8.5, resp.Totals.Month.SpendUSD, 1e-9)
	assert.Nil(t, resp.Fleet)
	require.Len(t, resp.History, 1)
	assert.Equal(t, spendDayPoint{Day: "2026-09-11", SpendUSD: 3.0}, resp.History[0])
	assert.Empty(t, resp.Caveats)
}

func TestBuildSpendOverview_RollupUnboundIsMemoryOnly(t *testing.T) {
	key := spendTestKey("iw:mine", "openai", "viewer@example.com")
	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", middleware.MaskKeyID(key.PK), "", "gpt", 0.25, 0.2, 0.05, 10, 5)

	resp := buildSpendOverview(spendInputs{
		scope: spendScopeMine, role: adminusers.RoleViewer, email: "viewer@example.com",
		day: "2026-09-11", month: "2026-09",
		keys: []*apikeys.APIKey{key}, snap: rec.Snapshot(),
		history: []adminrollup.CostDay{{Day: "2026-09-10", ByKeySpend: map[string]float64{middleware.MaskKeyID(key.PK): 9}}},
	})

	assert.False(t, resp.RollupAvailable)
	assert.Equal(t, []string{spendCaveatRollupOff}, resp.Caveats)
	require.Len(t, resp.Keys, 1)
	assert.Equal(t, "memory", resp.Keys[0].Today.Source)
	assert.Equal(t, "memory", resp.Keys[0].Month.Source)
	assert.InDelta(t, resp.Keys[0].Today.SpendUSD, resp.Keys[0].Month.SpendUSD, 1e-9)
	assert.Equal(t, "memory", resp.Totals.Today.Source)
	assert.Equal(t, "memory", resp.Totals.Month.Source)
	assert.Len(t, resp.History, 1, "archived history is not trusted when the rollup read failed")
}

func TestBuildSpendOverview_MineHistorySumsOwnedKeysOnly(t *testing.T) {
	key := spendTestKey("iw:mine", "openai", "viewer@example.com")
	masked := middleware.MaskKeyID(key.PK)
	resp := buildSpendOverview(spendInputs{
		scope: spendScopeMine, role: adminusers.RoleViewer, email: "viewer@example.com",
		day: "2026-09-11", month: "2026-09",
		keys: []*apikeys.APIKey{key}, snap: map[string]interface{}{}, rollupOK: true,
		history: []adminrollup.CostDay{
			{Day: "2026-09-09", Totals: adminrollup.KeyCostDayStats{SpendUSD: 100}, ByKeySpend: map[string]float64{masked: 4, "iw:someone-else": 96}},
			{Day: "2026-09-10", Totals: adminrollup.KeyCostDayStats{SpendUSD: 50}, ByKeySpend: map[string]float64{"iw:someone-else": 50}},
		},
	})

	require.Equal(t, []spendDayPoint{
		{Day: "2026-09-09", SpendUSD: 4},
		{Day: "2026-09-10", SpendUSD: 0},
		{Day: "2026-09-11", SpendUSD: 0},
	}, resp.History)
	assert.Equal(t, []string{spendCaveatHistoryTopN}, resp.Caveats)
}

func TestBuildSpendOverview_FleetInvariants(t *testing.T) {
	org := spendTestKey("iw:org", "openai", "")
	org.DailyCostLimit = 500
	personal := spendTestKey("iw:personal", "anthropic", "viewer@example.com")
	orgMasked, personalMasked := middleware.MaskKeyID(org.PK), middleware.MaskKeyID(personal.PK)

	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", orgMasked, "alice", "gpt", 1.0, 0.5, 0.5, 10, 10)
	rec.RecordRequest("anthropic", personalMasked, "bob", "claude", 2.0, 1.0, 1.0, 20, 20)
	// Request with no key id: counted by provider/user but attributable to no key row.
	rec.RecordRequest("gemini", "", "alice", "gemini", 0.5, 0.25, 0.25, 5, 5)

	resp := buildSpendOverview(spendInputs{
		scope: spendScopeFleet, role: adminusers.RoleEditor, email: "editor@example.com",
		day: "2026-09-11", month: "2026-09",
		keys: []*apikeys.APIKey{org, personal}, snap: rec.Snapshot(),
		rollupOK:    true,
		historyDays: 30,
		redisByProvider: map[string]adminrollup.KeyCostDayStats{
			"openai": {SpendUSD: 1.5, Requests: 3}, // another pod saw more
		},
		redisByUser: map[string]adminrollup.KeyCostDayStats{"user:carol": {SpendUSD: 0.1, Requests: 1}},
		monthByKey:  map[string]float64{orgMasked: 10, personalMasked: 20, "iw:deleted-key": 5},
		history: []adminrollup.CostDay{
			{Day: "2026-08-31", Totals: adminrollup.KeyCostDayStats{SpendUSD: 99}, ByProvider: map[string]adminrollup.KeyCostDayStats{"openai": {SpendUSD: 99}}},
			{Day: "2026-09-10", Totals: adminrollup.KeyCostDayStats{SpendUSD: 6}, ByProvider: map[string]adminrollup.KeyCostDayStats{"openai": {SpendUSD: 6}}, ByUserSpend: map[string]float64{"user:alice": 6}},
		},
		personalCap: 2000,
	})

	require.NotNil(t, resp.Fleet)
	require.Len(t, resp.Keys, 2)
	byID := map[string]spendKeyRow{}
	for _, row := range resp.Keys {
		byID[row.KeyID] = row
	}
	assert.Equal(t, "viewer@example.com", byID[personalMasked].OwnerEmail)
	assert.Equal(t, &spendCapResponse{Period: "daily", Cents: 500}, byID[orgMasked].Cap)
	assert.Equal(t, &spendCapResponse{Period: "monthly", Cents: 2000}, byID[personalMasked].Cap)

	var providersToday, keysToday float64
	for _, p := range resp.Providers {
		providersToday += p.Today.SpendUSD
	}
	for _, k := range resp.Keys {
		keysToday += k.Today.SpendUSD
	}
	assert.InDelta(t, resp.Totals.Today.SpendUSD, providersToday, 1e-9, "totals.today is the provider sum")
	assert.InDelta(t, 1.5+2.0+0.5, resp.Totals.Today.SpendUSD, 1e-9, "openai max-merged to the fleet value")
	assert.InDelta(t, resp.Totals.Today.SpendUSD, keysToday+resp.Fleet.Unattributed.Today.SpendUSD, 1e-9)
	assert.InDelta(t, 1.0, resp.Fleet.Unattributed.Today.SpendUSD, 1e-9, "keyless gemini request plus the openai fleet delta")

	assert.InDelta(t, 35, resp.Totals.Month.SpendUSD, 1e-9, "month total covers keys no longer in the registry")
	assert.InDelta(t, 5, resp.Fleet.Unattributed.Month.SpendUSD, 1e-9)

	provByName := map[string]spendProviderRow{}
	for _, p := range resp.Providers {
		provByName[p.Name] = p
	}
	assert.InDelta(t, 1.5+6, provByName["openai"].Month.SpendUSD, 1e-9, "only this month's archives count")
	assert.Equal(t, "redislive", provByName["openai"].Today.Source, "memory and redis both contributed")
	assert.Equal(t, "redislive", provByName["gemini"].Today.Source)

	require.Len(t, resp.Fleet.Users, 3)
	assert.Equal(t, "user:alice", resp.Fleet.Users[0].Scope)
	assert.InDelta(t, 1.5+6, resp.Fleet.Users[0].Month.SpendUSD, 1e-9)

	assert.Equal(t, []spendDayPoint{
		{Day: "2026-08-31", SpendUSD: 99},
		{Day: "2026-09-10", SpendUSD: 6},
		{Day: "2026-09-11", SpendUSD: 4.0},
	}, resp.History)
	assert.ElementsMatch(t, []string{spendCaveatHistoryTopN}, resp.Caveats)
}

func TestBuildSpendOverview_FleetUnattributedNeverNegative(t *testing.T) {
	key := spendTestKey("iw:org", "openai", "")
	masked := middleware.MaskKeyID(key.PK)
	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", masked, "", "gpt", 3.0, 1.5, 1.5, 10, 10)

	resp := buildSpendOverview(spendInputs{
		scope: spendScopeFleet, role: adminusers.RoleAdmin, email: "admin@example.com",
		day: "2026-09-02", month: "2026-09",
		keys: []*apikeys.APIKey{key}, snap: rec.Snapshot(), rollupOK: true, historyDays: 30,
		// Per-key row is ahead of the provider row (e.g. recent-event backfill).
		redisByKey:      map[string]adminrollup.KeyCostDayStats{masked: {SpendUSD: 5, Requests: 20}},
		redisByProvider: map[string]adminrollup.KeyCostDayStats{"openai": {SpendUSD: 3, Requests: 10}},
	})

	require.NotNil(t, resp.Fleet)
	assert.InDelta(t, 5, resp.Keys[0].Today.SpendUSD, 1e-9)
	assert.Zero(t, resp.Fleet.Unattributed.Today.SpendUSD)
	assert.Zero(t, resp.Fleet.Unattributed.Today.Requests)
	assert.Zero(t, resp.Fleet.Unattributed.Month.SpendUSD)
	assert.NotContains(t, resp.Caveats, spendCaveatProviderMonthPartial, "day 2 needs one archived day and we serve 30")
}

func TestBuildSpendOverview_ProviderMonthPartialCaveat(t *testing.T) {
	resp := buildSpendOverview(spendInputs{
		scope: spendScopeFleet, role: adminusers.RoleEditor, email: "e@example.com",
		day: "2026-09-25", month: "2026-09", snap: map[string]interface{}{},
		rollupOK: true, historyDays: 7,
	})
	assert.Equal(t, []string{spendCaveatProviderMonthPartial}, resp.Caveats)
}

func TestResolveSpendCap(t *testing.T) {
	personal := spendTestKey("iw:p", "openai", "v@example.com")
	assert.Equal(t, &spendCapResponse{Period: "monthly", Cents: 2000}, resolveSpendCap(personal, 2000))
	personal.MonthlyCostLimit = 1000
	assert.Equal(t, &spendCapResponse{Period: "monthly", Cents: 1000}, resolveSpendCap(personal, 2000))

	org := spendTestKey("iw:o", "openai", "")
	assert.Nil(t, resolveSpendCap(org, 2000), "org keys without limits are uncapped")
	org.DailyCostLimit = 300
	assert.Equal(t, &spendCapResponse{Period: "daily", Cents: 300}, resolveSpendCap(org, 2000))
	org.MonthlyCostLimit = 9000
	assert.Equal(t, &spendCapResponse{Period: "monthly", Cents: 9000}, resolveSpendCap(org, 2000), "monthly wins over daily")
}

func spendOverviewRequest(t *testing.T, h *handler, email string) (*httptest.ResponseRecorder, spendOverviewResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.handleSpendOverview(rec, authenticatedRequestAs(t, h, email, http.MethodGet, "/admin/api/spend-overview", nil))
	var body spendOverviewResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	}
	return rec, body
}

func TestHandleSpendOverview_ViewerSeesOnlyOwnKeys(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "viewer@example.com", adminusers.RoleViewer)
	require.NoError(t, err)
	mine, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-mine", "mine", 0, apikeys.KeyCreateMeta{})
	require.NoError(t, err)
	_, err = store.CreatePersonalKey(ctx, "other@example.com", "openai", "sk-other", "theirs", 0, apikeys.KeyCreateMeta{})
	require.NoError(t, err)
	_, err = store.CreateKey(ctx, "anthropic", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	costRec := coststats.NewRecorder()
	costRec.RecordRequest("openai", middleware.MaskKeyID(mine.PK), "u", "gpt", 0.02, 0.01, 0.01, 20, 10)
	h.deps.CostSummary = costRec.Snapshot
	unmeteredRec := unmeteredstats.NewRecorder()
	unmeteredRec.RecordRequest(middleware.MaskKeyID(mine.PK), "/v1/embeddings")
	unmeteredRec.RecordRequest(middleware.MaskKeyID(mine.PK), "/v1/embeddings")
	unmeteredRec.RecordRequest("iw:someone-else", "/v1/models")
	h.deps.UnmeteredSummary = unmeteredRec.Snapshot

	rec, body := spendOverviewRequest(t, h, "viewer@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, spendScopeMine, body.Scope)
	assert.Nil(t, body.Fleet)
	require.Len(t, body.Keys, 1)
	assert.Equal(t, middleware.MaskKeyID(mine.PK), body.Keys[0].KeyID)
	assert.Equal(t, &spendCapResponse{Period: "monthly", Cents: 2000}, body.Keys[0].Cap)
	assert.InDelta(t, 0.02, body.Keys[0].Today.SpendUSD, 1e-9)
	wantUnmetered := unmeteredResponse{Source: "memory", Requests: 2, Endpoints: []unmeteredEndpointResponse{{Endpoint: "/v1/embeddings", Requests: 2}}}
	assert.Equal(t, wantUnmetered, body.Keys[0].Unmetered)
	assert.Equal(t, wantUnmetered, body.Unmetered)
	require.Len(t, body.Providers, 1)
	assert.Equal(t, "openai", body.Providers[0].Name)
	assert.Contains(t, body.Caveats, spendCaveatRollupOff)
}

func TestHandleSpendOverview_EditorGetsFleetAndMatchesKeyStats(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()
	_, err := h.deps.UserStore.CreateUser(ctx, "editor@example.com", adminusers.RoleEditor)
	require.NoError(t, err)
	org, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)
	personal, err := store.CreatePersonalKey(ctx, "viewer@example.com", "anthropic", "sk-v", "mine", 0, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	day := time.Now().UTC().Format("2006-01-02")
	month := day[:7]
	rollup := testAdminRollupStore(t)
	orgMasked := middleware.MaskKeyID(org.PK)
	require.NoError(t, rollup.ApplyDelta(ctx, adminrollup.MetricCost, day, adminrollup.Delta{
		Totals: map[string]float64{"spend_usd": 0.42, "requests": 2},
		Dimensions: map[string]map[string]float64{
			"by_key":      {adminrollup.DimMemberField(orgMasked, "spend_usd"): 0.42, adminrollup.DimMemberField(orgMasked, "requests"): 2},
			"by_provider": {adminrollup.DimMemberField("openai", "spend_usd"): 0.42, adminrollup.DimMemberField("openai", "requests"): 2},
			"by_user":     {adminrollup.DimMemberField("user:alice", "spend_usd"): 0.42},
		},
	}))
	require.NoError(t, rollup.ApplyMonthlyKeySpend(ctx, adminrollup.MetricCost, month, orgMasked, 3.0))

	costRec := coststats.NewRecorder()
	costRec.RecordRequest("anthropic", middleware.MaskKeyID(personal.PK), "bob", "claude", 0.1, 0.05, 0.05, 10, 10)
	h.deps.AdminRollupStore = rollup
	h.deps.CostSummary = costRec.Snapshot
	unmeteredRec := unmeteredstats.NewRecorder()
	unmeteredRec.BindRollup(rollup, adminrollup.NewPersister(rollup, adminrollup.MetricUnmetered))
	unmeteredRec.RecordRequest(orgMasked, "/v1/chat/completions (HTTP 401)")
	unmeteredRec.RecordRequest("", "/v1/models")
	unmeteredRec.FlushRollup()
	h.deps.UnmeteredSummary = unmeteredRec.Snapshot

	rec, body := spendOverviewRequest(t, h, "editor@example.com")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, spendScopeFleet, body.Scope)
	assert.True(t, body.RollupAvailable)
	require.NotNil(t, body.Fleet)
	require.Len(t, body.Keys, 2)
	assert.Equal(t, orgMasked, body.Keys[0].KeyID)
	assert.Equal(t, "viewer@example.com", body.Keys[1].OwnerEmail)
	assert.InDelta(t, 3.0, body.Totals.Month.SpendUSD, 1e-9)
	assert.Len(t, body.Fleet.Users, 2)
	assert.Equal(t, unmeteredResponse{Source: "redis", Requests: 2, Endpoints: []unmeteredEndpointResponse{
		{Endpoint: "/v1/chat/completions (HTTP 401)", Requests: 1},
		{Endpoint: "/v1/models", Requests: 1},
	}}, body.Unmetered)

	// The row a user sees on the landing page must equal the key detail page.
	statsRec, stats := keyStatsRequest(t, h, "editor@example.com", org.PK)
	require.Equal(t, http.StatusOK, statsRec.Code)
	assert.Equal(t, stats.CostToday, body.Keys[0].Today)
	assert.Equal(t, stats.CostMonth, body.Keys[0].Month)
	assert.Equal(t, int64(1), stats.UnmeteredToday.Requests)
	assert.Equal(t, stats.UnmeteredToday, body.Keys[0].Unmetered)
}

func TestHandleSpendOverview_KeyStoreUnavailable(t *testing.T) {
	h, _ := testAdminHandler(t)
	h.deps.APIKeyStore = nil
	rec, _ := spendOverviewRequest(t, h, "admin@example.com")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
