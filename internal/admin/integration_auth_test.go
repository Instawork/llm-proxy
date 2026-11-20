package admin

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/integrationtoken"
	"github.com/Instawork/llm-proxy/internal/notify"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testInitHashEnv  = "TEST_LLM_PROXY_INTEGRATION_INIT_SHA256"
	testOtherHashEnv = "TEST_LLM_PROXY_INTEGRATION_OTHER_SHA256"
	testTicketHosts  = "TEST_LLM_PROXY_INTEGRATION_TICKET_URL_HOSTS"
)

var allIntegrationScopes = []string{
	config.IntegrationScopeKeyRequestsCreate,
	config.IntegrationScopeKeyRequestsRead,
	config.IntegrationScopeKeyRequestsCancel,
}

// integrationFixture routes requests through the real RegisterRoutes table
// with integrations enabled for two callers, "init" and "other". Tokens are
// generated per test, kept in memory, and never printed.
type integrationFixture struct {
	t          *testing.T
	router     *mux.Router
	h          *handler
	sender     *fakeSender
	logs       *syncBuffer
	initToken  string
	initHash   string
	otherToken string
}

func newIntegrationFixture(t *testing.T, mutate func(*config.IntegrationsConfig)) *integrationFixture {
	t.Helper()
	h, _ := testAdminHandler(t)
	withTestProvisioner(t, h, "openai")

	initToken, initHash, err := integrationtoken.New("init")
	require.NoError(t, err)
	otherToken, otherHash, err := integrationtoken.New("other")
	require.NoError(t, err)
	t.Setenv(testInitHashEnv, initHash)
	t.Setenv(testOtherHashEnv, otherHash)
	t.Setenv(testTicketHosts, "app.init.inc")

	ic := config.IntegrationsConfig{
		Enabled:                  true,
		RequestsPerMinute:        600,
		MaxDailyCostLimitCents:   4000,
		MaxMonthlyCostLimitCents: 100000,
		TicketURLHosts:           "${" + testTicketHosts + "}",
		Callers: []config.IntegrationCaller{
			{ID: "init", TokenSHA256Env: testInitHashEnv, Scopes: allIntegrationScopes},
			{ID: "other", TokenSHA256Env: testOtherHashEnv, Scopes: allIntegrationScopes},
		},
	}
	if mutate != nil {
		mutate(&ic)
	}
	h.deps.YAMLConfig.Features.AdminDashboard.Integrations = ic

	logs := &syncBuffer{}
	h.deps.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store, err := apikeys.NewStore(apikeys.StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true, Logger: h.deps.Logger})
	require.NoError(t, err)
	h.deps.APIKeyStore = store
	sender := newFakeSender()
	h.deps.Notifier = notify.NewNotifier(sender, h.deps.UserStore, nil, "https://llm.example.com", h.deps.Logger)

	r := mux.NewRouter()
	RegisterRoutes(r, *h.deps)
	return &integrationFixture{
		t: t, router: r, h: h, sender: sender, logs: logs,
		initToken: initToken, initHash: initHash, otherToken: otherToken,
	}
}

func (f *integrationFixture) do(token, method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(f.t, err)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func TestIntegrationAuth_RejectsBadCredentials(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	path := "/admin/api/integrations/key-requests/does-not-exist"

	for name, header := range map[string]string{
		"missing header":  "",
		"not bearer":      "Basic " + f.initToken,
		"wrong prefix":    "Bearer sk-iw-" + f.initToken,
		"unknown token":   "Bearer " + integrationtoken.Prefix + "init_not-a-real-token",
		"hash as token":   "Bearer " + f.initHash,
		"lowercase sheme": "bearer " + f.initToken,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			f.router.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.JSONEq(t, `{"error":"unauthorized"}`, rec.Body.String())
		})
	}

	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, path, nil).Code, "a valid token gets past auth")
}

func TestIntegrationAuth_ScopeRequired(t *testing.T) {
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) {
		ic.Callers[0].Scopes = []string{config.IntegrationScopeKeyRequestsRead}
	})
	rec := f.do(f.initToken, http.MethodPost, "/admin/api/integrations/key-requests", validIntegrationBody("INIT-1"))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = f.do(f.initToken, http.MethodPost, "/admin/api/integrations/key-requests/x/cancel", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = f.do(f.initToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestIntegrationAuth_RateLimitedPerCaller(t *testing.T) {
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) { ic.RequestsPerMinute = 2 })
	path := "/admin/api/integrations/key-requests/x"

	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, path, nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, path, nil).Code)
	rec := f.do(f.initToken, http.MethodGet, path, nil)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))

	assert.Equal(t, http.StatusNotFound, f.do(f.otherToken, http.MethodGet, path, nil).Code, "buckets are per caller")
}

func TestIntegrationAuth_CallerWithoutHashEnvIsSkipped(t *testing.T) {
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) {
		ic.Callers[1].TokenSHA256Env = "TEST_LLM_PROXY_INTEGRATION_UNSET_SHA256"
	})
	assert.Equal(t, http.StatusUnauthorized, f.do(f.otherToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil).Code)
	assert.Contains(t, f.logs.String(), "caller=other")
	assert.NotContains(t, f.logs.String(), f.initHash)
}

func TestIntegrationAuth_RotationAcceptsEveryListedHash(t *testing.T) {
	newToken, newHash, err := integrationtoken.New("init")
	require.NoError(t, err)
	f := newIntegrationFixture(t, nil)
	t.Setenv(testInitHashEnv, newHash+","+f.initHash)
	r := mux.NewRouter()
	RegisterRoutes(r, *f.h.deps)
	f.router = r

	assert.Equal(t, http.StatusNotFound, f.do(newToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil).Code)
	assert.Equal(t, http.StatusNotFound, f.do(f.initToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil).Code)
}

func TestIntegrationAuth_BearerTokenDoesNotOpenSessionRoutes(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	for _, path := range []string{"/admin/api/key-requests", "/admin/api/me", "/admin/api/keys"} {
		assert.Equal(t, http.StatusUnauthorized, f.do(f.initToken, http.MethodGet, path, nil).Code, path)
	}
}

func TestIntegrationAuth_SessionCookieDoesNotOpenIntegrationRoutes(t *testing.T) {
	f := newIntegrationFixture(t, nil)
	req := authenticatedRequestAs(t, f.h, "admin@example.com", http.MethodGet, "/admin/api/integrations/key-requests/x", nil)
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestIntegrationAuth_DisabledRoutesNotMounted(t *testing.T) {
	f := newIntegrationFixture(t, func(ic *config.IntegrationsConfig) { ic.Enabled = false })

	rec := f.do(f.initToken, http.MethodPost, "/admin/api/integrations/key-requests", validIntegrationBody("INIT-1"))
	assert.NotEqual(t, http.StatusCreated, rec.Code)
	assert.NotEqual(t, http.StatusOK, rec.Code)
	rec = f.do(f.initToken, http.MethodGet, "/admin/api/integrations/key-requests/x", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, f.logs.String(), "integrations API enabled")

	reqs, err := f.h.deps.APIKeyStore.ListKeyRequests(t.Context(), "")
	require.NoError(t, err)
	assert.Empty(t, reqs)
}
