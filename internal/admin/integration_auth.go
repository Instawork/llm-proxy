package admin

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/integrationtoken"
	"github.com/Instawork/llm-proxy/internal/middleware"
)

const defaultIntegrationRequestsPerMinute = 60

type integrationCaller struct {
	id     string
	hashes [][32]byte
	scopes map[string]bool
}

// integrationAuth authenticates /admin/api/integrations/* callers by bearer
// token. It never sees a session cookie and session routes never see it.
type integrationAuth struct {
	callers []integrationCaller
	limiter *middleware.TokenBucketLimiter
	logger  *slog.Logger
}

type integrationCallerKey struct{}

// newIntegrationAuth loads each caller's token hash from the env var named
// in config. A disabled caller, or one whose env var is unset or malformed,
// is skipped with a log line that names only the caller id.
func newIntegrationAuth(cfg config.IntegrationsConfig, getenv func(string) string, logger *slog.Logger) *integrationAuth {
	rpm := cfg.RequestsPerMinute
	if rpm <= 0 {
		rpm = defaultIntegrationRequestsPerMinute
	}
	a := &integrationAuth{
		limiter: middleware.NewTokenBucketLimiter(float64(rpm)/60, float64(rpm)),
		logger:  logger,
	}
	for _, c := range cfg.Callers {
		if c.Disabled {
			logger.Info("admin: integration caller disabled in config", "caller", c.ID)
			continue
		}
		hashes, err := integrationtoken.ParseHashes(getenv(c.TokenSHA256Env))
		if err != nil {
			logger.Warn("admin: integration caller skipped: token hash env var unset or invalid", "caller", c.ID)
			continue
		}
		scopes := make(map[string]bool, len(c.Scopes))
		for _, s := range c.Scopes {
			scopes[s] = true
		}
		a.callers = append(a.callers, integrationCaller{id: c.ID, hashes: hashes, scopes: scopes})
	}
	return a
}

// requireCaller resolves the bearer token to a caller, rate-limits per
// caller, and stores the caller on the request context. Every configured
// hash is compared in constant time so timing can't reveal which caller,
// if any, a token is close to.
func (a *integrationAuth) requireCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !strings.HasPrefix(raw, integrationtoken.Prefix) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		sum := integrationtoken.Hash(raw)
		var match *integrationCaller
		for i := range a.callers {
			for _, h := range a.callers[i].hashes {
				if subtle.ConstantTimeCompare(sum[:], h[:]) == 1 {
					match = &a.callers[i]
				}
			}
		}
		if match == nil {
			a.logger.Warn("admin: integration request rejected: unknown token", "client_ip", middleware.TrustedClientIP(r))
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		if !a.limiter.Allow(match.id, time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), integrationCallerKey{}, match)))
	})
}

// scope wraps fn so it only runs for callers granted scope s.
func (a *integrationAuth) scope(s string, fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := integrationCallerFrom(r.Context())
		if caller == nil || !caller.scopes[s] {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		fn(w, r)
	})
}

func integrationCallerFrom(ctx context.Context) *integrationCaller {
	c, _ := ctx.Value(integrationCallerKey{}).(*integrationCaller)
	return c
}
