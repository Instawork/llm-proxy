package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gorilla/sessions"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	sessionName       = "llm-proxy-admin-session"
	sessionUserEmail  = "user_email"
	sessionUserName   = "user_name"
	sessionOAuthState = "oauth_state"
)

type authConfig struct {
	clientID      string
	clientSecret  string
	sessionSecret string
	allowedDomain string
	redirectURL   string
}

type authenticator struct {
	oauthConfig       *oauth2.Config
	verifier          *oidc.IDTokenVerifier
	sessionStore      *sessions.CookieStore
	allowedDomain     string
	redirectURLEnv    string
	devBypass         bool
	devFrontendOrigin string
	logger            *slog.Logger
}

func newAuthenticator(logger *slog.Logger, adminCfg config.AdminDashboardConfig) (*authenticator, error) {
	allowedDomain := adminCfg.AllowedDomain
	if allowedDomain == "" {
		allowedDomain = "instawork.com"
	}

	sessionSecret := os.Getenv("LLM_PROXY_ADMIN_SESSION_SECRET")
	if sessionSecret == "" {
		if adminCfg.DevBypassLogin {
			sessionSecret = "dev-local-session-secret-not-for-prod"
			logger.Warn("admin auth: using default dev session secret; set LLM_PROXY_ADMIN_SESSION_SECRET for a stable cookie")
		} else {
			return nil, fmt.Errorf("LLM_PROXY_ADMIN_SESSION_SECRET is required")
		}
	}

	sessionStore := sessions.NewCookieStore([]byte(sessionSecret))
	sessionStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   os.Getenv("LLM_PROXY_ADMIN_SESSION_SECURE") == "1",
	}

	auth := &authenticator{
		sessionStore:      sessionStore,
		allowedDomain:     allowedDomain,
		devBypass:         adminCfg.DevBypassLogin,
		devFrontendOrigin: adminCfg.DevCORSOrigin,
		logger:            logger,
	}

	clientID := os.Getenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID")
	clientSecret := os.Getenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		if adminCfg.DevBypassLogin {
			logger.Warn("admin auth: Google OAuth not configured; dev bypass login only")
			return auth, nil
		}
		return nil, fmt.Errorf("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID and LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET are required")
	}

	cfg, err := loadAuthConfig(allowedDomain, clientID, clientSecret, sessionSecret)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("oidc provider: %w", err)
	}

	auth.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.clientID})
	auth.oauthConfig = &oauth2.Config{
		ClientID:     cfg.clientID,
		ClientSecret: cfg.clientSecret,
		RedirectURL:  cfg.redirectURL,
		Endpoint:     google.Endpoint,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
	auth.redirectURLEnv = cfg.redirectURL
	return auth, nil
}

func loadAuthConfig(allowedDomain, clientID, clientSecret, sessionSecret string) (authConfig, error) {
	domain := os.Getenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN")
	if domain == "" {
		if allowedDomain != "" {
			domain = allowedDomain
		} else {
			domain = "instawork.com"
		}
	}

	return authConfig{
		clientID:      clientID,
		clientSecret:  clientSecret,
		sessionSecret: sessionSecret,
		allowedDomain: domain,
		redirectURL:   os.Getenv("LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL"),
	}, nil
}

func (a *authenticator) redirectURL(r *http.Request) string {
	if a.redirectURLEnv != "" {
		return a.redirectURLEnv
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/admin/auth/callback", scheme, r.Host)
}

func (a *authenticator) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.oauthConfig == nil {
		http.Error(w, "google oauth not configured", http.StatusServiceUnavailable)
		return
	}

	state, err := randomState()
	if err != nil {
		a.logger.Error("admin auth: failed to generate oauth state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	session, _ := a.sessionStore.Get(r, sessionName)
	session.Values[sessionOAuthState] = state
	if err := session.Save(r, w); err != nil {
		a.logger.Error("admin auth: failed to save oauth state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	cfg := *a.oauthConfig
	cfg.RedirectURL = a.redirectURL(r)
	url := cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, url, http.StatusFound)
}

type devLoginRequest struct {
	Redirect string `json:"redirect"`
}

func (a *authenticator) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	if !a.devBypass {
		http.NotFound(w, r)
		return
	}

	email := os.Getenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL")
	if email == "" {
		email = "dev@instawork.com"
	}

	redirectTarget := r.URL.Query().Get("redirect")
	if redirectTarget == "" && r.Body != nil {
		var req devLoginRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err == nil {
			redirectTarget = req.Redirect
		}
	}

	session, _ := a.sessionStore.Get(r, sessionName)
	session.Values[sessionUserEmail] = email
	session.Values[sessionUserName] = "Dev User"
	if err := session.Save(r, w); err != nil {
		a.logger.Error("admin auth: dev login session save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	a.logger.Info("admin auth: dev bypass login", "email", email)

	if safe, target := sanitizeRedirect(redirectTarget, a.devFrontendOrigin); safe {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": target, "email": email})
		return
	}

	defaultRedirect := "/admin/"
	if a.devFrontendOrigin != "" {
		defaultRedirect = strings.TrimRight(a.devFrontendOrigin, "/") + "/admin/"
	}
	writeJSON(w, http.StatusOK, map[string]string{"redirect": defaultRedirect, "email": email})
}

func sanitizeRedirect(raw, devOrigin string) (bool, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false, ""
	}
	if devOrigin != "" {
		dev, err := url.Parse(devOrigin)
		if err == nil && strings.EqualFold(u.Scheme, dev.Scheme) && strings.EqualFold(u.Host, dev.Host) {
			return true, raw
		}
	}
	if strings.EqualFold(u.Hostname(), "localhost") || strings.EqualFold(u.Hostname(), "127.0.0.1") {
		return true, raw
	}
	return false, ""
}

func (a *authenticator) handleCallback(w http.ResponseWriter, r *http.Request) {
	if a.oauthConfig == nil {
		http.Error(w, "google oauth not configured", http.StatusUnauthorized)
		return
	}

	session, err := a.sessionStore.Get(r, sessionName)
	if err != nil {
		a.logger.Error("admin auth: session read failed", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	expectedState, _ := session.Values[sessionOAuthState].(string)
	if expectedState == "" || r.URL.Query().Get("state") != expectedState {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	delete(session.Values, sessionOAuthState)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	cfg := *a.oauthConfig
	cfg.RedirectURL = a.redirectURL(r)
	token, err := cfg.Exchange(r.Context(), code)
	if err != nil {
		a.logger.Error("admin auth: token exchange failed", "error", err)
		http.Error(w, "oauth exchange failed", http.StatusUnauthorized)
		return
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "missing id_token", http.StatusUnauthorized)
		return
	}

	idToken, err := a.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		a.logger.Error("admin auth: id token verification failed", "error", err)
		http.Error(w, "invalid id token", http.StatusUnauthorized)
		return
	}

	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		HD            string `json:"hd"`
	}
	if err := idToken.Claims(&claims); err != nil {
		a.logger.Error("admin auth: claims parse failed", "error", err)
		http.Error(w, "invalid claims", http.StatusUnauthorized)
		return
	}

	if !claims.EmailVerified || claims.Email == "" {
		http.Error(w, "email not verified", http.StatusForbidden)
		return
	}
	if !a.isAllowedUser(claims.Email, claims.HD) {
		a.logger.Warn("admin auth: domain rejected", "email", claims.Email, "hd", claims.HD)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	session.Values[sessionUserEmail] = claims.Email
	session.Values[sessionUserName] = claims.Name
	if err := session.Save(r, w); err != nil {
		a.logger.Error("admin auth: session save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	redirectTarget := "/admin/"
	if a.devFrontendOrigin != "" {
		redirectTarget = strings.TrimRight(a.devFrontendOrigin, "/") + "/admin/"
	}
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

func (a *authenticator) handleLogout(w http.ResponseWriter, r *http.Request) {
	session, err := a.sessionStore.Get(r, sessionName)
	if err == nil {
		session.Options.MaxAge = -1
		_ = session.Save(r, w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *authenticator) currentUser(r *http.Request) (*UserResponse, error) {
	session, err := a.sessionStore.Get(r, sessionName)
	if err != nil {
		return nil, err
	}
	email, _ := session.Values[sessionUserEmail].(string)
	if email == "" {
		return nil, fmt.Errorf("not authenticated")
	}
	name, _ := session.Values[sessionUserName].(string)
	return &UserResponse{Email: email, Name: name}, nil
}

func (a *authenticator) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.currentUser(r); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *authenticator) isAllowedUser(email, hd string) bool {
	domain := a.allowedDomain
	if hd != "" {
		return strings.EqualFold(hd, domain)
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	return strings.EqualFold(email[at+1:], domain)
}

func randomState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
