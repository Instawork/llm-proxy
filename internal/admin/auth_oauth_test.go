package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

const testOAuthClientID = "test-oauth-client-id"

type testOIDCServer struct {
	t           *testing.T
	server      *httptest.Server
	issuer      string
	privateKey  *rsa.PrivateKey
	kid         string
	lastCode    string
	tokenClaims idTokenClaims
}

func newTestOIDCServer(t *testing.T) *testOIDCServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	td := &testOIDCServer{
		t:          t,
		privateKey: key,
		kid:        "test-signing-key",
		tokenClaims: idTokenClaims{
			email:         "alice@example.com",
			emailVerified: true,
			name:          "Alice Example",
			hd:            "example.com",
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", td.serveDiscovery)
	mux.HandleFunc("/jwks", td.serveJWKS)
	mux.HandleFunc("/token", td.serveToken)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "authorize stub", http.StatusNotImplemented)
	})
	td.server = httptest.NewServer(mux)
	td.issuer = td.server.URL
	t.Cleanup(td.server.Close)
	return td
}

func (td *testOIDCServer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"issuer":                 td.issuer,
		"authorization_endpoint": td.issuer + "/authorize",
		"token_endpoint":         td.issuer + "/token",
		"jwks_uri":               td.issuer + "/jwks",
	})
}

func (td *testOIDCServer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	n := base64.RawURLEncoding.EncodeToString(td.privateKey.PublicKey.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"keys": []map[string]string{
			{
				"kty": "RSA",
				"kid": td.kid,
				"alg": "RS256",
				"use": "sig",
				"n":   n,
				"e":   e,
			},
		},
	})
}

func (td *testOIDCServer) serveToken(w http.ResponseWriter, r *http.Request) {
	require.NoError(td.t, r.ParseForm())
	td.lastCode = r.Form.Get("code")
	if td.lastCode == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	idToken := td.mintIDToken(td.tokenClaims)
	writeJSON(w, http.StatusOK, map[string]string{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		"id_token":     idToken,
	})
}

type idTokenClaims struct {
	email         string
	emailVerified bool
	name          string
	picture       string
	hd            string
}

func (td *testOIDCServer) mintIDToken(claims idTokenClaims) string {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: td.privateKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", td.kid),
	)
	require.NoError(td.t, err)

	now := time.Now()
	raw, err := jwt.Signed(signer).
		Claims(jwt.Claims{
			Issuer:   td.issuer,
			Subject:  claims.email,
			Audience: jwt.Audience{testOAuthClientID},
			Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt: jwt.NewNumericDate(now),
		}).
		Claims(map[string]interface{}{
			"email":          claims.email,
			"email_verified": claims.emailVerified,
			"name":           claims.name,
			"picture":        claims.picture,
			"hd":             claims.hd,
		}).
		Serialize()
	require.NoError(td.t, err)
	return raw
}

func newTestOAuthAuthenticator(t *testing.T, oidcSrv *testOIDCServer, allowedDomain string, userStore ...*adminusers.Store) *authenticator {
	t.Helper()
	provider, err := oidc.NewProvider(context.Background(), oidcSrv.issuer)
	require.NoError(t, err)

	sessionStore := sessions.NewCookieStore([]byte("test-secret-at-least-32-bytes-long"))
	sessionStore.Options = &sessions.Options{Path: "/", MaxAge: 3600, HttpOnly: true}

	auth := &authenticator{
		oauthConfig: &oauth2.Config{
			ClientID:     testOAuthClientID,
			ClientSecret: "test-secret",
			RedirectURL:  "http://localhost/admin/auth/callback",
			Endpoint: oauth2.Endpoint{
				AuthURL:  oidcSrv.issuer + "/authorize",
				TokenURL: oidcSrv.issuer + "/token",
			},
			Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier:          provider.Verifier(&oidc.Config{ClientID: testOAuthClientID}),
		sessionStore:      sessionStore,
		allowedDomain:     allowedDomain,
		devFrontendOrigin: "http://localhost:5173",
		logger:            testLogger(),
	}
	if len(userStore) > 0 {
		auth.userStore = userStore[0]
	}
	return auth
}

func saveOAuthStateSession(t *testing.T, auth *authenticator, w http.ResponseWriter, r *http.Request, state string) {
	t.Helper()
	session, err := auth.sessionStore.Get(r, sessionName)
	require.NoError(t, err)
	session.Values[sessionOAuthState] = state
	require.NoError(t, session.Save(r, w))
}

func sessionCookies(t *testing.T, rec *httptest.ResponseRecorder) []*http.Cookie {
	t.Helper()
	return rec.Result().Cookies()
}

func TestLoadAuthConfig_EnvOverrides(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN", "instawork.com")
	t.Setenv("LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL", "https://llm.example.com/admin/auth/callback")

	cfg, err := loadAuthConfig("example.com", "cid", "csecret", "ssecret")
	require.NoError(t, err)
	assert.Equal(t, "instawork.com", cfg.allowedDomain)
	assert.Equal(t, "https://llm.example.com/admin/auth/callback", cfg.redirectURL)
	assert.Equal(t, "cid", cfg.clientID)
}

func TestNewAuthenticator_AllowedDomainHonorsEnvOverride(t *testing.T) {
	// Regression: the env override must reach auth.allowedDomain (what
	// isAllowedUser checks), not just cfg.allowedDomain. Previously the env was
	// resolved into cfg but auth.allowedDomain kept the YAML value, so every
	// real-org login was rejected as "forbidden". DevBypassLogin keeps this
	// network-free (returns before the Google OIDC call).
	t.Setenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN", "instawork.com")
	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
		AllowedDomain:  "example.com",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "instawork.com", auth.allowedDomain)
	assert.True(t, auth.isAllowedUser("alice@instawork.com", "instawork.com"))
	assert.False(t, auth.isAllowedUser("alice@example.com", "example.com"))
}

func TestIsAllowedUser(t *testing.T) {
	auth := &authenticator{allowedDomain: "example.com"}
	assert.True(t, auth.isAllowedUser("alice@example.com", ""))
	assert.True(t, auth.isAllowedUser("bob@example.com", "example.com"))
	assert.False(t, auth.isAllowedUser("alice@other.com", ""))
	assert.True(t, auth.isAllowedUser("alice@other.com", "example.com"), "hd match wins over email domain")
	assert.False(t, auth.isAllowedUser("alice@example.com", "other.com"))
	assert.False(t, auth.isAllowedUser("not-an-email", ""))
}

func TestRandomState_UniqueAndNonEmpty(t *testing.T) {
	a, err := randomState()
	require.NoError(t, err)
	b, err := randomState()
	require.NoError(t, err)
	assert.NotEmpty(t, a)
	assert.NotEmpty(t, b)
	assert.NotEqual(t, a, b)
}

func TestRedirectURL_FromRequest(t *testing.T) {
	auth := &authenticator{}
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	req.Host = "llm.instawork.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	assert.Equal(t, "https://llm.instawork.com/admin/auth/callback", auth.redirectURL(req))
}

func TestRedirectURL_EnvWins(t *testing.T) {
	auth := &authenticator{redirectURLEnv: "https://fixed.example/callback"}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	assert.Equal(t, "https://fixed.example/callback", auth.redirectURL(req))
}

func TestHandleLogin_RedirectsWithState(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	auth.handleLogin(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	loc := rec.Header().Get("Location")
	require.NotEmpty(t, loc)
	u, err := url.Parse(loc)
	require.NoError(t, err)
	assert.Contains(t, u.String(), "/authorize")
	assert.NotEmpty(t, u.Query().Get("state"))

	cookies := sessionCookies(t, rec)
	require.NotEmpty(t, cookies)
}

func TestHandleLogin_OAuthNotConfigured(t *testing.T) {
	auth := &authenticator{logger: testLogger(), sessionStore: sessions.NewCookieStore([]byte("test-secret-at-least-32-bytes-long"))}
	rec := httptest.NewRecorder()
	auth.handleLogin(rec, httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHandleCallback_HappyPath(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")
	state := "test-oauth-state-12345"

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	saveOAuthStateSession(t, auth, loginRec, loginReq, state)

	cbRec := httptest.NewRecorder()
	cbReq := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+state+"&code=auth-code-xyz", nil)
	for _, c := range sessionCookies(t, loginRec) {
		cbReq.AddCookie(c)
	}
	auth.handleCallback(cbRec, cbReq)

	require.Equal(t, http.StatusFound, cbRec.Code)
	assert.Equal(t, "http://localhost:5173/admin/", cbRec.Header().Get("Location"))
	assert.Equal(t, "auth-code-xyz", oidcSrv.lastCode)

	meReq := httptest.NewRequest(http.MethodGet, "/admin/api/me", nil)
	for _, c := range sessionCookies(t, cbRec) {
		meReq.AddCookie(c)
	}
	user, err := auth.currentUser(meReq)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", user.Email)
	assert.Equal(t, "Alice Example", user.Name)
}

func TestHandleCallback_EnsureUser(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	userStore := testAdminUserStore(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com", userStore)
	state := "test-oauth-state-ensure-user"

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil)
	saveOAuthStateSession(t, auth, loginRec, loginReq, state)

	cbRec := httptest.NewRecorder()
	cbReq := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+state+"&code=auth-code-xyz", nil)
	for _, c := range sessionCookies(t, loginRec) {
		cbReq.AddCookie(c)
	}
	auth.handleCallback(cbRec, cbReq)
	require.Equal(t, http.StatusFound, cbRec.Code)

	got, err := userStore.GetUser(context.Background(), "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, adminusers.RoleViewer, got.Role)
	assert.Equal(t, "Alice Example", got.Name)
}

func TestHandleCallback_InvalidState(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state=wrong&code=abc", nil)
	saveOAuthStateSession(t, auth, rec, req, "expected-state")
	for _, c := range sessionCookies(t, rec) {
		req.AddCookie(c)
	}

	cbRec := httptest.NewRecorder()
	auth.handleCallback(cbRec, req)
	assert.Equal(t, http.StatusBadRequest, cbRec.Code)
}

func TestHandleCallback_MissingCode(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")
	state := "state-abc"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+state, nil)
	saveOAuthStateSession(t, auth, rec, req, state)
	for _, c := range sessionCookies(t, rec) {
		req.AddCookie(c)
	}

	cbRec := httptest.NewRecorder()
	auth.handleCallback(cbRec, req)
	assert.Equal(t, http.StatusBadRequest, cbRec.Code)
}

func TestHandleCallback_DomainRejected(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	oidcSrv.tokenClaims = idTokenClaims{
		email:         "intruder@evil.com",
		emailVerified: true,
		hd:            "evil.com",
	}
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")
	state := "state-deny"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+state+"&code=code", nil)
	saveOAuthStateSession(t, auth, rec, req, state)
	for _, c := range sessionCookies(t, rec) {
		req.AddCookie(c)
	}

	cbRec := httptest.NewRecorder()
	auth.handleCallback(cbRec, req)
	assert.Equal(t, http.StatusForbidden, cbRec.Code)
}

func TestHandleCallback_EmailNotVerified(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	oidcSrv.tokenClaims = idTokenClaims{
		email:         "alice@example.com",
		emailVerified: false,
		hd:            "example.com",
	}
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")
	state := "state-unverified"

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state="+state+"&code=code", nil)
	saveOAuthStateSession(t, auth, rec, req, state)
	for _, c := range sessionCookies(t, rec) {
		req.AddCookie(c)
	}

	cbRec := httptest.NewRecorder()
	auth.handleCallback(cbRec, req)
	assert.Equal(t, http.StatusForbidden, cbRec.Code)
}

func TestHandleLogout_ClearsSession(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")

	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/", nil)
	saveOAuthStateSession(t, auth, loginRec, loginReq, "s")
	session, _ := auth.sessionStore.Get(loginReq, sessionName)
	session.Values[sessionUserEmail] = "alice@example.com"
	require.NoError(t, session.Save(loginReq, loginRec))

	logoutRec := httptest.NewRecorder()
	logoutReq := httptest.NewRequest(http.MethodPost, "/admin/auth/logout", nil)
	for _, c := range sessionCookies(t, loginRec) {
		logoutReq.AddCookie(c)
	}
	auth.handleLogout(logoutRec, logoutReq)
	assert.Equal(t, http.StatusNoContent, logoutRec.Code)

	afterReq := httptest.NewRequest(http.MethodGet, "/admin/api/me", nil)
	for _, c := range sessionCookies(t, logoutRec) {
		afterReq.AddCookie(c)
	}
	_, err := auth.currentUser(afterReq)
	require.Error(t, err)
}

func TestRequireSession_BlocksUnauthenticated(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")

	called := false
	rec := httptest.NewRecorder()
	auth.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil))

	assert.False(t, called)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireSession_AllowsAuthenticated(t *testing.T) {
	oidcSrv := newTestOIDCServer(t)
	auth := newTestOAuthAuthenticator(t, oidcSrv, "example.com")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	session, _ := auth.sessionStore.Get(req, sessionName)
	session.Values[sessionUserEmail] = "alice@example.com"
	require.NoError(t, session.Save(req, rec))

	called := false
	apiRec := httptest.NewRecorder()
	apiReq := httptest.NewRequest(http.MethodGet, "/admin/api/keys", nil)
	for _, c := range sessionCookies(t, rec) {
		apiReq.AddCookie(c)
	}
	auth.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})).ServeHTTP(apiRec, apiReq)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, apiRec.Code)
}

func TestNewAuthenticator_DevBypassWithoutGoogleOAuth(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID", "")
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET", "")

	auth, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: true,
		AllowedDomain:  "example.com",
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, auth.oauthConfig)
	assert.True(t, auth.devBypass)
}

func TestHandleCallback_OAuthNotConfigured(t *testing.T) {
	auth := &authenticator{logger: testLogger(), sessionStore: sessions.NewCookieStore([]byte("test-secret-at-least-32-bytes-long"))}
	rec := httptest.NewRecorder()
	auth.handleCallback(rec, httptest.NewRequest(http.MethodGet, "/admin/auth/callback?state=x&code=y", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestNewAuthenticator_RequiresSessionSecretInProd(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "")
	_, err := newAuthenticator(testLogger(), config.AdminDashboardConfig{
		DevBypassLogin: false,
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LLM_PROXY_ADMIN_SESSION_SECRET")
}

func TestSanitizeRedirect(t *testing.T) {
	ok, target := sanitizeRedirect("http://localhost:5173/admin/", "http://localhost:5173")
	assert.True(t, ok)
	assert.Equal(t, "http://localhost:5173/admin/", target)

	ok, _ = sanitizeRedirect("https://evil.com/admin/", "http://localhost:5173")
	assert.False(t, ok)

	ok, _ = sanitizeRedirect("not-a-url", "")
	assert.False(t, ok)
}

func TestRegisterRoutes_OAuthLoginRoute(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")
	oidcSrv := newTestOIDCServer(t)

	provider, err := oidc.NewProvider(context.Background(), oidcSrv.issuer)
	require.NoError(t, err)

	yamlCfg := config.GetDefaultYAMLConfig()
	yamlCfg.Features.AdminDashboard.DevBypassLogin = false
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID", testOAuthClientID)
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET", "secret")
	t.Cleanup(func() {
		t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID", "")
		t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET", "")
	})

	// newAuthenticator hits real Google — register routes manually with test auth.
	auth := &authenticator{
		oauthConfig: &oauth2.Config{
			ClientID:     testOAuthClientID,
			ClientSecret: "secret",
			Endpoint: oauth2.Endpoint{
				AuthURL:  oidcSrv.issuer + "/authorize",
				TokenURL: oidcSrv.issuer + "/token",
			},
			Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier:      provider.Verifier(&oidc.Config{ClientID: testOAuthClientID}),
		sessionStore:  sessions.NewCookieStore([]byte("test-secret-at-least-32-bytes-long")),
		allowedDomain: "example.com",
		logger:        testLogger(),
	}

	r := mux.NewRouter()
	r.PathPrefix("/admin/auth").Subrouter().HandleFunc("/login", auth.handleLogin).Methods(http.MethodGet)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/auth/login", nil))
	assert.Equal(t, http.StatusFound, rec.Code)
}
