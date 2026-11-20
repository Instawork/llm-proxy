package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/integrationtoken"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/providers"
)

const (
	testFinchHashEnv    = "TEST_LLM_PROXY_INTEGRATION_FINCH_SHA256"
	testFinchEditor     = "finch.editor@example.com"
	agentLimitsPathRoot = "/admin/api/integrations/agent-limits/"
	agentSpendPath      = "/admin/api/integrations/agent-spend"
	testAgentLabel      = "finch-agent-123"
	testOtherAgentLabel = "finch-agent-456"
)

var finchScopes = []string{
	config.IntegrationScopeAgentLimitsRead,
	config.IntegrationScopeAgentLimitsWrite,
	config.IntegrationScopeAgentSpendRead,
}

type agentLimitsFixture struct {
	*integrationFixture
	finchToken string
	finchHash  string
	rollup     *adminrollup.Store
	redis      *miniredis.Miniredis
	resolver   *apikeys.AgentLimitResolver
}

func testAgentSpendLimitsConfig() config.AgentSpendLimitsConfig {
	return config.AgentSpendLimitsConfig{
		Enabled:                 true,
		Mode:                    config.AgentSpendModeObserve,
		UserPrefix:              "finch-agent-",
		DefaultSoftMonthlyCents: 10000,
		DefaultHardMonthlyCents: 20000,
		MaxHardMonthlyCents:     500000,
		LimitCacheTTLSeconds:    60,
	}
}

// newAgentLimitsFixture adds a "finch" caller with finchScopes (or scopes,
// when given) to the integration fixture, enables agent spend limits, and
// binds a miniredis-backed rollup store and a resolver over the key store.
func newAgentLimitsFixture(t *testing.T, mutate func(*config.AgentSpendLimitsConfig), scopes ...string) *agentLimitsFixture {
	t.Helper()
	if len(scopes) == 0 {
		scopes = finchScopes
	}
	finchToken, finchHash, err := integrationtoken.New("finch")
	require.NoError(t, err)
	t.Setenv(testFinchHashEnv, finchHash)
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) {
		ic.Callers = append(ic.Callers, config.IntegrationCaller{ID: "finch", TokenSHA256Env: testFinchHashEnv, Scopes: scopes})
	})

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rollup, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rollup.Close() })

	cfg := testAgentSpendLimitsConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	f.h.deps.YAMLConfig.Features.AgentSpendLimits = cfg
	f.h.deps.AdminRollupStore = rollup
	resolver := apikeys.NewAgentLimitResolver(f.h.deps.APIKeyStore, cfg)
	f.h.deps.AgentLimitResolver = resolver

	r := mux.NewRouter()
	RegisterRoutes(r, *f.h.deps)
	f.router = r
	return &agentLimitsFixture{integrationFixture: f, finchToken: finchToken, finchHash: finchHash, rollup: rollup, redis: mr, resolver: resolver}
}

func (f *agentLimitsFixture) seedSpend(t *testing.T, month, label string, usd float64) {
	t.Helper()
	require.NoError(t, f.rollup.ApplyMonthlyUserSpend(t.Context(), adminrollup.MetricCost, month, label, usd))
}

func (f *agentLimitsFixture) putRaw(t *testing.T, label, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, agentLimitsPathRoot+label, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.finchToken)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func decodeAgentLimit(t *testing.T, rec *httptest.ResponseRecorder) IntegrationAgentLimitResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp IntegrationAgentLimitResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func decodeAgentSpend(t *testing.T, rec *httptest.ResponseRecorder) IntegrationAgentSpendResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp IntegrationAgentSpendResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func thisMonth() string { return time.Now().UTC().Format("2006-01") }

func TestAgentLimitsRoute_ScopesAndAuth(t *testing.T) {
	f := newAgentLimitsFixture(t, nil, config.IntegrationScopeAgentLimitsRead)
	get := agentLimitsPathRoot + testAgentLabel
	put := map[string]any{"soft_monthly_cents": 5000, "updated_by": testFinchEditor}

	assert.Equal(t, http.StatusUnauthorized, f.do("", http.MethodGet, get, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(f.finchToken+"x", http.MethodGet, get, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(f.finchHash, http.MethodGet, agentSpendPath, nil).Code)

	assert.Equal(t, http.StatusOK, f.do(f.finchToken, http.MethodGet, get, nil).Code, "read scope opens GET")
	assert.Equal(t, http.StatusForbidden, f.do(f.finchToken, http.MethodPut, get, put).Code, "write scope required")
	assert.Equal(t, http.StatusForbidden, f.do(f.finchToken, http.MethodGet, agentSpendPath, nil).Code, "spend scope required")

	// "init" holds only key_requests:* scopes.
	assert.Equal(t, http.StatusForbidden, f.do(f.initToken, http.MethodGet, get, nil).Code)
	assert.Equal(t, http.StatusForbidden, f.do(f.initToken, http.MethodPut, get, put).Code)
	assert.Equal(t, http.StatusForbidden, f.do(f.initToken, http.MethodGet, agentSpendPath, nil).Code)

	_, err := f.h.deps.APIKeyStore.GetAgentLimit(t.Context(), testAgentLabel)
	assert.ErrorIs(t, err, apikeys.ErrAgentLimitNotFound, "a forbidden PUT stores nothing")
}

func TestAgentLimitsRoute_DisabledCallerIsRejected(t *testing.T) {
	finchToken, finchHash, err := integrationtoken.New("finch")
	require.NoError(t, err)
	t.Setenv(testFinchHashEnv, finchHash)
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) {
		ic.Callers = append(ic.Callers, config.IntegrationCaller{ID: "finch", TokenSHA256Env: testFinchHashEnv, Scopes: finchScopes, Disabled: true})
	})
	assert.Equal(t, http.StatusUnauthorized, f.do(finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil).Code)
	assert.Contains(t, f.logs.String(), "caller=finch")
}

func TestAgentLimitsRoute_GetDefaults(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	f.seedSpend(t, thisMonth(), testAgentLabel, 12.341)

	rec := f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil)
	resp := decodeAgentLimit(t, rec)
	assert.Equal(t, IntegrationAgentLimitResponse{
		Label:      testAgentLabel,
		Month:      thisMonth(),
		Mode:       "observe",
		SpendCents: 1235,
		SoftCents:  10000,
		HardCents:  20000,
		State:      "ok",
		Defaults:   IntegrationAgentLimitDefaults{SoftMonthlyCents: 10000, HardMonthlyCents: 20000, MaxHardMonthlyCents: 500000},
	}, resp)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Contains(t, raw, "override")
	assert.Nil(t, raw["override"], "override is null when no item exists")
}

func TestAgentLimitsRoute_PutPatchesAndClears(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)

	resp := decodeAgentLimit(t, f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":5000,"hard_monthly_cents":30000,"exempt":true,"updated_by":"Finch.Editor@Example.com"}`))
	assert.Equal(t, int64(5000), resp.SoftCents)
	assert.Equal(t, int64(30000), resp.HardCents)
	assert.True(t, resp.Exempt)
	require.NotNil(t, resp.Override)
	require.NotNil(t, resp.Override.SoftMonthlyCents)
	assert.Equal(t, int64(5000), *resp.Override.SoftMonthlyCents)
	require.NotNil(t, resp.Override.UpdatedBy)
	assert.Equal(t, testFinchEditor, *resp.Override.UpdatedBy, "stored lowercased")
	require.NotNil(t, resp.Override.UpdatedAt)
	firstUpdate := *resp.Override.UpdatedAt

	// An omitted key is unchanged; null clears back to the default.
	resp = decodeAgentLimit(t, f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":null,"exempt":null,"updated_by":"other.editor@example.com"}`))
	assert.Equal(t, int64(10000), resp.SoftCents, "cleared soft takes the default")
	assert.Equal(t, int64(30000), resp.HardCents, "omitted hard keeps its override")
	assert.False(t, resp.Exempt)
	require.NotNil(t, resp.Override)
	assert.Nil(t, resp.Override.SoftMonthlyCents)
	assert.Nil(t, resp.Override.Exempt)
	require.NotNil(t, resp.Override.HardMonthlyCents)
	assert.Equal(t, "other.editor@example.com", *resp.Override.UpdatedBy)
	assert.False(t, resp.Override.UpdatedAt.Before(firstUpdate))

	got := decodeAgentLimit(t, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil))
	assert.Equal(t, resp, got, "GET returns the shape PUT returned")

	var raw struct {
		Override map[string]any `json:"override"`
	}
	require.NoError(t, json.Unmarshal(f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil).Body.Bytes(), &raw))
	for _, k := range []string{"soft_monthly_cents", "hard_monthly_cents", "exempt", "updated_by", "updated_at"} {
		assert.Contains(t, raw.Override, k, "override.%s is always present", k)
	}
	assert.Nil(t, raw.Override["soft_monthly_cents"])
}

func TestAgentLimitsRoute_PutBounds(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"hard_monthly_cents":30000,"updated_by":"a@example.com"}`).Code)

	cases := map[string]struct {
		label, body string
		status      int
	}{
		"soft above hard":          {testAgentLabel, `{"soft_monthly_cents":30001,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"soft above default hard":  {testOtherAgentLabel, `{"soft_monthly_cents":20001,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"hard below stored soft":   {testAgentLabel, `{"hard_monthly_cents":9999,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"hard above max":           {testAgentLabel, `{"hard_monthly_cents":500001,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"negative soft":            {testAgentLabel, `{"soft_monthly_cents":-1,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"fractional cents":         {testAgentLabel, `{"soft_monthly_cents":10.5,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"exempt not bool":          {testAgentLabel, `{"exempt":"yes","updated_by":"a@example.com"}`, http.StatusBadRequest},
		"unknown field":            {testAgentLabel, `{"soft_cents":1,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"missing updated_by":       {testAgentLabel, `{"soft_monthly_cents":1}`, http.StatusBadRequest},
		"updated_by not email":     {testAgentLabel, `{"soft_monthly_cents":1,"updated_by":"finch"}`, http.StatusBadRequest},
		"updated_by two addresses": {testAgentLabel, `{"soft_monthly_cents":1,"updated_by":"a@example.com,b@example.com"}`, http.StatusBadRequest},
		"label without prefix":     {"agent-123", `{"soft_monthly_cents":1,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"label is only the prefix": {"finch-agent-", `{"soft_monthly_cents":1,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"label with reserved char": {"finch-agent-1%7C2", `{"soft_monthly_cents":1,"updated_by":"a@example.com"}`, http.StatusBadRequest},
		"body too large":           {testAgentLabel, `{"updated_by":"` + strings.Repeat("a", 5<<10) + `@example.com"}`, http.StatusRequestEntityTooLarge},
		"invalid json":             {testAgentLabel, `{`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.putRaw(t, tc.label, tc.body)
			assert.Equal(t, tc.status, rec.Code, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "a@example.com", "errors never echo updated_by")
		})
	}

	stored, err := f.h.deps.APIKeyStore.GetAgentLimit(t.Context(), testAgentLabel)
	require.NoError(t, err)
	require.NotNil(t, stored.HardMonthlyCents)
	assert.Equal(t, int64(30000), *stored.HardMonthlyCents, "no rejected PUT changed the item")
	_, err = f.h.deps.APIKeyStore.GetAgentLimit(t.Context(), testOtherAgentLabel)
	assert.ErrorIs(t, err, apikeys.ErrAgentLimitNotFound)

	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":25000,"updated_by":"a@example.com"}`).Code)
	rec := f.putRaw(t, testAgentLabel, `{"hard_monthly_cents":null,"updated_by":"a@example.com"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "clearing hard to the 20000 default would leave it below soft 25000")
	assert.Equal(t, http.StatusBadRequest, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+"agent-123", nil).Code, "GET checks the prefix too")
}

func TestAgentLimitsRoute_ModeAndState(t *testing.T) {
	f := newAgentLimitsFixture(t, func(c *config.AgentSpendLimitsConfig) { c.Mode = config.AgentSpendModeEnforce })
	month := thisMonth()

	f.seedSpend(t, month, "finch-agent-1", 99.99)
	f.seedSpend(t, month, "finch-agent-2", 100)
	f.seedSpend(t, month, "finch-agent-3", 250)
	require.Equal(t, http.StatusOK, f.putRaw(t, "finch-agent-3", `{"exempt":true,"updated_by":"a@example.com"}`).Code)

	for label, want := range map[string]string{"finch-agent-1": "ok", "finch-agent-2": "soft", "finch-agent-3": "hard", "finch-agent-4": "ok"} {
		resp := decodeAgentLimit(t, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+label, nil))
		assert.Equal(t, "enforce", resp.Mode)
		assert.Equal(t, want, resp.State, label)
	}
	resp := decodeAgentLimit(t, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+"finch-agent-3", nil))
	assert.True(t, resp.Exempt, "exempt agents still report their state")
	assert.Equal(t, int64(25000), resp.SpendCents)
	assert.Equal(t, "enforce", decodeAgentSpend(t, f.do(f.finchToken, http.MethodGet, agentSpendPath+"?month="+month+"&min_state=ok", nil)).Mode)
}

func TestAgentLimitsRoute_PutInvalidatesResolver(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)

	before, err := f.resolver.Resolve(t.Context(), testAgentLabel)
	require.NoError(t, err)
	assert.Equal(t, int64(20000), before.HardMonthlyCents, "default cached as a miss")

	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"hard_monthly_cents":15000,"updated_by":"a@example.com"}`).Code)

	after, err := f.resolver.Resolve(t.Context(), testAgentLabel)
	require.NoError(t, err)
	assert.Equal(t, int64(15000), after.HardMonthlyCents, "the write dropped the cached miss")
}

// The PUT must invalidate the same resolver the middleware reads, so a
// lowered limit blocks the very next call in this process instead of after
// limit_cache_ttl_seconds.
func TestAgentLimitsRoute_PutSeenByMiddlewareNextLookup(t *testing.T) {
	f := newAgentLimitsFixture(t, func(c *config.AgentSpendLimitsConfig) { c.Mode = config.AgentSpendModeEnforce })
	f.seedSpend(t, thisMonth(), testAgentLabel, 150)

	spend := coststats.NewRecorder()
	spend.BindRollup(f.rollup, adminrollup.NewPersister(f.rollup, adminrollup.MetricCost))
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	proxied := middleware.AgentSpendLimitMiddleware(pm, middleware.AgentSpendLimitOptions{
		Config: f.h.deps.YAMLConfig.Features.AgentSpendLimits,
		Limits: f.h.deps.AgentLimitResolver,
		Spend:  spend,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func() int {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
		req.Header.Set("X-User-ID", testAgentLabel)
		rec := httptest.NewRecorder()
		proxied.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, call(), "$150 is under the $200 default, now cached")
	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"hard_monthly_cents":12000,"updated_by":"a@example.com"}`).Code)
	assert.Equal(t, http.StatusPaymentRequired, call(), "the next lookup sees the lowered limit")

	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"exempt":true,"updated_by":"a@example.com"}`).Code)
	assert.Equal(t, http.StatusOK, call(), "and the exemption")
}

func TestAgentSpendRoute_ListsPrefixedLabelsAtOrAboveMinState(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	month := thisMonth()
	f.seedSpend(t, month, "finch-agent-1", 5)
	f.seedSpend(t, month, "finch-agent-2", 150)
	f.seedSpend(t, month, "finch-agent-3", 300)
	f.seedSpend(t, month, "finch-agent-4", 60)
	f.seedSpend(t, month, "Brain Agent", 900)
	f.seedSpend(t, month, "token:abcd1234", 900)
	require.Equal(t, http.StatusOK, f.putRaw(t, "finch-agent-4", `{"soft_monthly_cents":5000,"exempt":true,"updated_by":"a@example.com"}`).Code)
	require.Equal(t, http.StatusOK, f.putRaw(t, "finch-agent-9", `{"hard_monthly_cents":1,"soft_monthly_cents":0,"updated_by":"a@example.com"}`).Code)

	list := func(q string) IntegrationAgentSpendResponse {
		return decodeAgentSpend(t, f.do(f.finchToken, http.MethodGet, agentSpendPath+q, nil))
	}

	all := list("?month=" + month + "&min_state=ok")
	assert.Equal(t, month, all.Month)
	assert.Equal(t, "observe", all.Mode)
	assert.Equal(t, []IntegrationAgentSpendEntry{
		{Label: "finch-agent-1", SpendCents: 500, SoftCents: 10000, HardCents: 20000, State: "ok"},
		{Label: "finch-agent-2", SpendCents: 15000, SoftCents: 10000, HardCents: 20000, State: "soft"},
		{Label: "finch-agent-3", SpendCents: 30000, SoftCents: 10000, HardCents: 20000, State: "hard"},
		{Label: "finch-agent-4", SpendCents: 6000, SoftCents: 5000, HardCents: 20000, Exempt: true, State: "soft"},
	}, all.Agents, "only prefixed labels with spend, sorted, overrides applied")

	soft := list("?month=" + month + "&min_state=soft")
	require.Len(t, soft.Agents, 3)
	assert.Equal(t, "finch-agent-2", soft.Agents[0].Label)

	hard := list("?month=" + month + "&min_state=hard")
	require.Len(t, hard.Agents, 1)
	assert.Equal(t, "finch-agent-3", hard.Agents[0].Label)

	assert.Len(t, list("").Agents, 4, "both parameters default: current month, min_state ok")

	prev := time.Now().UTC().AddDate(0, -1, 0)
	prevMonth := time.Date(prev.Year(), prev.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
	if prevMonth == month {
		prevMonth = time.Now().UTC().AddDate(0, 0, -32).Format("2006-01")
	}
	empty := f.do(f.finchToken, http.MethodGet, agentSpendPath+"?month="+prevMonth+"&min_state=ok", nil)
	assert.JSONEq(t, `{"month":"`+prevMonth+`","mode":"observe","agents":[]}`, empty.Body.String(), "agents is [] not null")

	for _, q := range []string{"?month=2026-13", "?month=2026-1", "?month=202611", "?month=x&min_state=ok", "?min_state=warn", "?min_state=HARD"} {
		assert.Equal(t, http.StatusBadRequest, f.do(f.finchToken, http.MethodGet, agentSpendPath+q, nil).Code, q)
	}
}

func TestAgentLimitsRoute_FeatureDisabled(t *testing.T) {
	f := newAgentLimitsFixture(t, func(c *config.AgentSpendLimitsConfig) { c.Enabled = false })
	for _, rec := range []*httptest.ResponseRecorder{
		f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil),
		f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":1,"updated_by":"a@example.com"}`),
		f.do(f.finchToken, http.MethodGet, agentSpendPath, nil),
	} {
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.JSONEq(t, `{"error":"agent spend limits are disabled"}`, rec.Body.String())
	}
}

func TestAgentLimitsRoute_SpendStoreDown(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	f.redis.Close()
	assert.Equal(t, http.StatusServiceUnavailable, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil).Code)
	assert.Equal(t, http.StatusServiceUnavailable, f.do(f.finchToken, http.MethodGet, agentSpendPath, nil).Code)
}

func TestAgentLimitsRoute_SpendStoreUnbound(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	f.h.deps.AdminRollupStore = nil
	r := mux.NewRouter()
	RegisterRoutes(r, *f.h.deps)
	f.router = r
	rec := f.do(f.finchToken, http.MethodGet, agentSpendPath, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"error":"agent spend counter unavailable"}`, rec.Body.String())
}

// Agent-limit logs must never carry the bearer token, its hash, or the
// editor email Finch sends as updated_by.
func TestAgentLimitsRoute_LogsOmitTokensAndEmails(t *testing.T) {
	f := newAgentLimitsFixture(t, nil)
	f.seedSpend(t, thisMonth(), testAgentLabel, 150)

	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":5000,"updated_by":"`+testFinchEditor+`"}`).Code)
	require.Equal(t, http.StatusOK, f.putRaw(t, testAgentLabel, `{"soft_monthly_cents":null,"updated_by":"`+testFinchEditor+`"}`).Code)
	require.Equal(t, http.StatusBadRequest, f.putRaw(t, testAgentLabel, `{"hard_monthly_cents":1,"updated_by":"`+testFinchEditor+`"}`).Code)
	require.Equal(t, http.StatusBadRequest, f.putRaw(t, testAgentLabel, `{"soft_cents":1,"updated_by":"`+testFinchEditor+`"}`).Code)
	require.Equal(t, http.StatusOK, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil).Code)
	require.Equal(t, http.StatusOK, f.do(f.finchToken, http.MethodGet, agentSpendPath+"?month="+thisMonth()+"&min_state=soft", nil).Code)
	require.Equal(t, http.StatusUnauthorized, f.do(f.finchToken+"x", http.MethodGet, agentSpendPath, nil).Code)
	f.redis.Close()
	require.Equal(t, http.StatusServiceUnavailable, f.do(f.finchToken, http.MethodGet, agentLimitsPathRoot+testAgentLabel, nil).Code)

	out := f.logs.String()
	require.Contains(t, out, "integration agent limit updated")
	require.Contains(t, out, "integration request rejected")
	require.Contains(t, out, "integration agent spend read failed")
	assert.NotContains(t, out, f.finchToken)
	assert.NotContains(t, out, f.finchToken[:20], "not even a token prefix beyond the caller id")
	assert.NotContains(t, out, f.finchHash)
	assert.NotContains(t, out, "finch.editor")
	assert.NotContains(t, out, "@example.com")
}
