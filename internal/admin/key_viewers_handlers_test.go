package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routedAdmin exercises the real route table (method matching, route order,
// role gates) rather than calling handlers directly.
type routedAdmin struct {
	t      *testing.T
	router *mux.Router
	store  *apikeys.Store
	users  *adminusers.Store
	h      *handler
	cost   *coststats.Recorder
}

func newRoutedAdmin(t *testing.T) *routedAdmin {
	t.Helper()
	h, store := testAdminHandler(t)
	cost := coststats.NewRecorder()
	h.deps.CostSummary = cost.Snapshot
	r := mux.NewRouter()
	RegisterRoutes(r, *h.deps)
	return &routedAdmin{t: t, router: r, store: store, users: h.deps.UserStore, h: h, cost: cost}
}

func (a *routedAdmin) addUser(email string, role adminusers.Role) {
	a.t.Helper()
	_, err := a.users.CreateUser(context.Background(), email, role)
	require.NoError(a.t, err)
}

func (a *routedAdmin) do(email, method, path string, body any) *httptest.ResponseRecorder {
	a.t.Helper()
	a.t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", email)

	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	a.router.ServeHTTP(loginRec, loginReq)
	require.Equal(a.t, http.StatusOK, loginRec.Code)

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(a.t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range loginRec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.router.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), rec.Body.String())
	return v
}

type sharedFixture struct {
	*routedAdmin
	shared    *apikeys.APIKey
	unshared  *apikeys.APIKey
	personal  *apikeys.APIKey
	sharedURL string
}

// newSharedFixture builds: an org key shared with grantee@ (and requested by
// requester@), an org key shared with nobody, and a personal key for owner@.
func newSharedFixture(t *testing.T) *sharedFixture {
	t.Helper()
	a := newRoutedAdmin(t)
	ctx := context.Background()
	for _, u := range []struct {
		email string
		role  adminusers.Role
	}{
		{"grantee@example.com", adminusers.RoleViewer},
		{"requester@example.com", adminusers.RoleViewer},
		{"stranger@example.com", adminusers.RoleViewer},
		{"owner@example.com", adminusers.RoleViewer},
		{"editor@example.com", adminusers.RoleEditor},
	} {
		a.addUser(u.email, u.role)
	}

	shared, err := a.store.CreateKeyWithMeta(ctx, "openai", "sk-upstream-secret", "shared service key", 500, 0,
		map[string]string{"team": "payments"}, nil,
		apikeys.KeyCreateMeta{RequesterEmail: "requester@example.com"},
		apikeys.KeyRateLimits{RPM: 60})
	require.NoError(t, err)
	_, err = a.store.AddKeyViewers(ctx, shared.PK, []string{"grantee@example.com", "other-grantee@example.com"})
	require.NoError(t, err)

	unshared, err := a.store.CreateKey(ctx, "anthropic", "sk-ant-secret", "private org key", 0, nil, nil)
	require.NoError(t, err)
	personal, err := a.store.CreatePersonalKey(ctx, "owner@example.com", "gemini", "sk-own", "mine", 1000, apikeys.KeyCreateMeta{})
	require.NoError(t, err)

	a.cost.RecordRequest("openai", middleware.MaskKeyID(shared.PK), "some-user", "gpt-4o", 0.07, 0.03, 0.04, 20, 10)

	return &sharedFixture{
		routedAdmin: a,
		shared:      shared,
		unshared:    unshared,
		personal:    personal,
		sharedURL:   "/admin/api/keys/" + url.PathEscape(shared.PK),
	}
}

func assertNoSecrets(t *testing.T, body string, f *sharedFixture) {
	t.Helper()
	assert.NotContains(t, body, f.shared.PK)
	assert.NotContains(t, body, "sk-upstream-secret")
	assert.NotContains(t, body, "requester@example.com")
	assert.NotContains(t, body, "other-grantee@example.com")
	assert.NotContains(t, body, "payments")
	assert.NotContains(t, body, "some-user")
}

func TestViewerListShowsSharedKeysReadOnly(t *testing.T) {
	f := newSharedFixture(t)

	for _, email := range []string{"grantee@example.com", "requester@example.com"} {
		t.Run(email, func(t *testing.T) {
			rec := f.do(email, http.MethodGet, "/admin/api/keys", nil)
			require.Equal(t, http.StatusOK, rec.Code)
			keys := decode[[]KeyResponse](t, rec)
			require.Len(t, keys, 1)

			got := keys[0]
			masked := apikeys.MaskKeyID(f.shared.PK)
			assert.Equal(t, masked, got.Key)
			assert.Equal(t, masked, got.MaskedKeyID)
			assert.Equal(t, "viewer", got.Access)
			assert.True(t, got.ReadOnly)
			assert.Equal(t, "shared service key", got.Description)
			assert.Equal(t, int64(500), got.DailyCostLimit)
			assert.Empty(t, got.OwnerEmail)
			assert.Empty(t, got.RequesterEmail)
			assert.Empty(t, got.ViewerEmails)
			assert.Empty(t, got.ActualKey)
			assert.Empty(t, got.Tags)
			assert.Zero(t, got.RateLimitRPM)
			require.NotNil(t, got.Spend)
			assert.InDelta(t, 0.07, got.Spend.Today.SpendUSD, 1e-9)
			assert.Equal(t, int64(1), got.Spend.Today.Requests)
			require.NotNil(t, got.Spend.Cap)
			assert.Equal(t, "daily", got.Spend.Cap.Period)
			assertNoSecrets(t, rec.Body.String(), f)
		})
	}
}

func TestViewerListMergesPersonalAndSharedKeys(t *testing.T) {
	f := newSharedFixture(t)
	ctx := context.Background()
	_, err := f.store.AddKeyViewers(ctx, f.unshared.PK, []string{"owner@example.com"})
	require.NoError(t, err)

	rec := f.do("owner@example.com", http.MethodGet, "/admin/api/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	byAccess := map[string]KeyResponse{}
	for _, k := range decode[[]KeyResponse](t, rec) {
		byAccess[k.Access] = k
	}
	require.Len(t, byAccess, 2)
	assert.Equal(t, f.personal.PK, byAccess["owner"].Key, "owner keeps the full credential on their own key")
	assert.False(t, byAccess["owner"].ReadOnly)
	assert.True(t, byAccess["viewer"].ReadOnly)
	assert.NotContains(t, rec.Body.String(), f.unshared.PK)
}

func TestViewerListCarriesSpendOnOwnPersonalKey(t *testing.T) {
	f := newSharedFixture(t)
	f.cost.RecordRequest("gemini", middleware.MaskKeyID(f.personal.PK), "u", "gemini-pro", 0.5, 0.2, 0.3, 40, 20)

	rec := f.do("owner@example.com", http.MethodGet, "/admin/api/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var own *KeyResponse
	for _, k := range decode[[]KeyResponse](t, rec) {
		if k.Access == "owner" {
			k := k
			own = &k
		}
	}
	require.NotNil(t, own)
	require.NotNil(t, own.Spend, "viewers see what their own key has cost in the list")
	assert.InDelta(t, 0.5, own.Spend.Today.SpendUSD, 1e-9)
	assert.InDelta(t, 0.5, own.Spend.Month.SpendUSD, 1e-9)
	require.NotNil(t, own.Spend.Cap)
	assert.Equal(t, int64(1000), own.Spend.Cap.Cents)
	assert.Equal(t, f.personal.PK, own.Key, "owner keeps the full credential on their own key")
}

func TestEditorListHasNoSpendBlock(t *testing.T) {
	f := newSharedFixture(t)
	rec := f.do("editor@example.com", http.MethodGet, "/admin/api/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	for _, k := range decode[[]KeyResponse](t, rec) {
		assert.Nil(t, k.Spend)
	}
}

func TestUnrelatedViewerSeesNoSharedKeys(t *testing.T) {
	f := newSharedFixture(t)
	rec := f.do("stranger@example.com", http.MethodGet, "/admin/api/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, decode[[]KeyResponse](t, rec))
}

func TestEditorListMarksManagerAndShowsViewers(t *testing.T) {
	f := newSharedFixture(t)
	rec := f.do("editor@example.com", http.MethodGet, "/admin/api/keys", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	for _, k := range decode[[]KeyResponse](t, rec) {
		assert.Equal(t, "manager", k.Access)
		assert.False(t, k.ReadOnly)
		if k.Key == f.shared.PK {
			assert.ElementsMatch(t, []string{"grantee@example.com", "other-grantee@example.com"}, k.ViewerEmails)
		}
	}
}

func TestGranteeCanReadKeyDetailByMaskedIDOrFullKey(t *testing.T) {
	f := newSharedFixture(t)
	masked := apikeys.MaskKeyID(f.shared.PK)

	for name, id := range map[string]string{"masked": masked, "full key": f.shared.PK} {
		t.Run(name, func(t *testing.T) {
			rec := f.do("requester@example.com", http.MethodGet, "/admin/api/keys/"+url.PathEscape(id), nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got := decode[KeyResponse](t, rec)
			assert.Equal(t, masked, got.Key)
			assert.Equal(t, "viewer", got.Access)
			assert.True(t, got.ReadOnly)
			assert.NotEmpty(t, got.BaseURL)
			require.NotNil(t, got.Spend)
			assertNoSecrets(t, rec.Body.String(), f)
		})
	}
}

func TestGranteeStatsOmitContentSafetyTelemetry(t *testing.T) {
	f := newSharedFixture(t)
	rec := f.do("grantee@example.com", http.MethodGet, "/admin/api/keys/"+url.PathEscape(f.shared.PK)+"/stats", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	stats := decode[keyStatsResponse](t, rec)
	assert.InDelta(t, 0.07, stats.CostToday.SpendUSD, 1e-9)
	assert.Empty(t, stats.RecentPII)
	assert.Empty(t, stats.RateUsage)
	assert.Zero(t, stats.PIIToday.Detections)
	assert.NotContains(t, rec.Body.String(), f.shared.PK)
}

func TestGranteeWritesAreForbidden(t *testing.T) {
	f := newSharedFixture(t)
	keyURL := f.sharedURL

	writes := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"patch", http.MethodPatch, keyURL, map[string]any{"description": "hijacked"}},
		{"patch enabled", http.MethodPatch, keyURL, map[string]any{"enabled": false}},
		{"delete", http.MethodDelete, keyURL, nil},
		{"share", http.MethodPost, "/admin/api/share", map[string]string{"key": f.shared.PK}},
		{"list viewers", http.MethodGet, keyURL + "/viewers", nil},
		{"add viewers", http.MethodPost, keyURL + "/viewers", map[string]any{"emails": []string{"grantee@example.com"}}},
		{"remove viewer", http.MethodDelete, keyURL + "/viewers/other-grantee@example.com", nil},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			rec := f.do("grantee@example.com", w.method, w.path, w.body)
			assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		})
	}

	record, err := f.store.GetKeyRecord(context.Background(), f.shared.PK)
	require.NoError(t, err)
	assert.Equal(t, "shared service key", record.Description)
	assert.True(t, record.Enabled)
	assert.Len(t, record.ViewerEmails, 2)
}

func TestShareWriteRoutesAreNotReachableToGrantees(t *testing.T) {
	f := newSharedFixture(t)
	rec := f.do("grantee@example.com", http.MethodPost, "/admin/api/share", map[string]string{"key": apikeys.MaskKeyID(f.shared.PK)})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// A viewer with no grant must not be able to tell "exists but not shared with
// me" from "does not exist", on any key route, including the viewer routes.
func TestUnsharedKeyIndistinguishableFromMissingForViewers(t *testing.T) {
	f := newSharedFixture(t)
	missing := apikeys.KeyPrefix + "0000000000000000000000000000000000000000000000000000000000000000"

	routes := []struct {
		name   string
		method string
		suffix string
		body   any
	}{
		{"get", http.MethodGet, "", nil},
		{"stats", http.MethodGet, "/stats", nil},
		{"patch", http.MethodPatch, "", map[string]any{"description": "x"}},
		{"delete", http.MethodDelete, "", nil},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			real := f.do("stranger@example.com", rt.method, "/admin/api/keys/"+url.PathEscape(f.shared.PK)+rt.suffix, rt.body)
			gone := f.do("stranger@example.com", rt.method, "/admin/api/keys/"+url.PathEscape(missing)+rt.suffix, rt.body)
			assert.Equal(t, http.StatusNotFound, real.Code)
			assert.Equal(t, gone.Code, real.Code)
			assert.JSONEq(t, gone.Body.String(), real.Body.String())
		})
	}

	t.Run("share", func(t *testing.T) {
		real := f.do("stranger@example.com", http.MethodPost, "/admin/api/share", map[string]string{"key": f.shared.PK})
		gone := f.do("stranger@example.com", http.MethodPost, "/admin/api/share", map[string]string{"key": missing})
		assert.Equal(t, http.StatusNotFound, real.Code)
		assert.JSONEq(t, gone.Body.String(), real.Body.String())
	})

	t.Run("masked id", func(t *testing.T) {
		rec := f.do("stranger@example.com", http.MethodGet, "/admin/api/keys/"+url.PathEscape(apikeys.MaskKeyID(f.shared.PK)), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("another user's personal key", func(t *testing.T) {
		rec := f.do("grantee@example.com", http.MethodGet, "/admin/api/keys/"+url.PathEscape(f.personal.PK), nil)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestViewerRoutesRequireEditor(t *testing.T) {
	f := newSharedFixture(t)
	keyURL := f.sharedURL + "/viewers"
	// A viewer who is neither grantee nor owner is stopped by the role gate
	// before the key is even looked up.
	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, keyURL, nil},
		{http.MethodPost, keyURL, map[string]any{"emails": []string{"x@example.com"}}},
		{http.MethodDelete, keyURL + "/grantee@example.com", nil},
	} {
		rec := f.do("stranger@example.com", tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s", tc.method, tc.path)
	}
}

func TestEditorManagesViewers(t *testing.T) {
	f := newSharedFixture(t)
	viewersURL := f.sharedURL + "/viewers"

	t.Run("list", func(t *testing.T) {
		rec := f.do("editor@example.com", http.MethodGet, viewersURL, nil)
		require.Equal(t, http.StatusOK, rec.Code)
		got := decode[KeyViewersResponse](t, rec)
		assert.Equal(t, []string{"grantee@example.com", "other-grantee@example.com"}, got.Viewers)
		assert.Equal(t, "requester@example.com", got.RequesterEmail)
	})

	t.Run("add grants access immediately", func(t *testing.T) {
		before := f.do("stranger@example.com", http.MethodGet, "/admin/api/keys", nil)
		assert.Empty(t, decode[[]KeyResponse](t, before))

		rec := f.do("editor@example.com", http.MethodPost, viewersURL,
			map[string]any{"emails": []string{" Stranger@Example.com ", "stranger@example.com"}})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, decode[KeyViewersResponse](t, rec).Viewers, "stranger@example.com")

		after := f.do("stranger@example.com", http.MethodGet, "/admin/api/keys", nil)
		require.Len(t, decode[[]KeyResponse](t, after), 1)
	})

	t.Run("remove revokes access immediately", func(t *testing.T) {
		rec := f.do("editor@example.com", http.MethodDelete, viewersURL+"/Stranger@example.com", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.NotContains(t, decode[KeyViewersResponse](t, rec).Viewers, "stranger@example.com")

		after := f.do("stranger@example.com", http.MethodGet, "/admin/api/keys", nil)
		assert.Empty(t, decode[[]KeyResponse](t, after))
		detail := f.do("stranger@example.com", http.MethodGet, f.sharedURL, nil)
		assert.Equal(t, http.StatusNotFound, detail.Code)
	})

	t.Run("works with masked key id", func(t *testing.T) {
		masked := "/admin/api/keys/" + url.PathEscape(apikeys.MaskKeyID(f.shared.PK)) + "/viewers"
		rec := f.do("editor@example.com", http.MethodGet, masked, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestAddKeyViewersValidation(t *testing.T) {
	f := newSharedFixture(t)
	viewersURL := f.sharedURL + "/viewers"

	cases := []struct {
		name string
		path string
		body any
		want int
	}{
		{"no emails", viewersURL, map[string]any{"emails": []string{}}, http.StatusBadRequest},
		{"not an email", viewersURL, map[string]any{"emails": []string{"nope"}}, http.StatusBadRequest},
		{"multiple at signs", viewersURL, map[string]any{"emails": []string{"a@b@example.com"}}, http.StatusBadRequest},
		{"empty local part", viewersURL, map[string]any{"emails": []string{"@example.com"}}, http.StatusBadRequest},
		{"display name", viewersURL, map[string]any{"emails": []string{"Eve <eve@example.com>"}}, http.StatusBadRequest},
		{"disallowed domain", viewersURL, map[string]any{"emails": []string{"x@evil.test"}}, http.StatusBadRequest},
		{"one bad email rejects the batch", viewersURL, map[string]any{"emails": []string{"ok@example.com", "x@evil.test"}}, http.StatusBadRequest},
		{"personal key", "/admin/api/keys/" + url.PathEscape(f.personal.PK) + "/viewers", map[string]any{"emails": []string{"x@example.com"}}, http.StatusBadRequest},
		{"missing key", "/admin/api/keys/" + url.PathEscape(apikeys.KeyPrefix+"0000000000000000000000000000000000000000000000000000000000000000") + "/viewers", map[string]any{"emails": []string{"x@example.com"}}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do("editor@example.com", http.MethodPost, tc.path, tc.body)
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}

	record, err := f.store.GetKeyRecord(context.Background(), f.shared.PK)
	require.NoError(t, err)
	assert.NotContains(t, record.ViewerEmails, "ok@example.com")

	t.Run("too many viewers", func(t *testing.T) {
		emails := make([]string, apikeys.MaxKeyViewers+1)
		for i := range emails {
			emails[i] = "u" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "@example.com"
		}
		rec := f.do("editor@example.com", http.MethodPost, viewersURL, map[string]any{"emails": emails})
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestApprovedKeyRequesterCanViewTheirKey(t *testing.T) {
	f := newSharedFixture(t)
	// The key-request modal links to /keys/<created_key>; before per-key
	// viewers that was a 404 for the requester.
	rec := f.do("requester@example.com", http.MethodGet, "/admin/api/keys/"+url.PathEscape(f.shared.PK), nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "viewer", decode[KeyResponse](t, rec).Access)
}

func TestAllowedDomainPrecedence(t *testing.T) {
	h, _ := testAdminHandler(t)
	assert.Equal(t, "example.com", h.allowedDomain(), "default")

	t.Setenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN", "corp.test")
	assert.Equal(t, "corp.test", h.allowedDomain(), "env overrides default")

	h.deps.YAMLConfig = nil
	assert.Equal(t, "corp.test", h.allowedDomain(), "env applies without YAML")
}
