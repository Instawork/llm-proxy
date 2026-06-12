package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/Instawork/llm-proxy/internal/ratelimitstats"
)

// RateLimitingMiddleware enforces rate limits using the provided limiter.
// It does a provisional token reservation based on estimation and reconciles after response parsing.
// stats is optional; when set it records decisions for admin rollups and row history.
func RateLimitingMiddleware(pm *providers.ProviderManager, cfg *config.YAMLConfig, limiter ratelimit.RateLimiter, stats *ratelimitstats.Recorder) func(http.Handler) http.Handler {
	if limiter == nil || cfg == nil || !cfg.Features.RateLimiting.Enabled {
		return func(next http.Handler) http.Handler { return next }
	}

	estCfg := providers.YAMLConfigEstimationAdapter{
		MaxSampleBytes:        cfg.Features.RateLimiting.Estimation.MaxSampleBytes,
		BytesPerToken:         cfg.Features.RateLimiting.Estimation.BytesPerToken,
		CharsPerToken:         cfg.Features.RateLimiting.Estimation.CharsPerToken,
		ProviderCharsPerToken: cfg.Features.RateLimiting.Estimation.ProviderCharsPerToken,
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prov := GetProviderFromRequest(pm, r)
			if prov == nil {
				next.ServeHTTP(w, r)
				return
			}

			// Scope keys
			userID := ExtractUserIDFromRequest(r, prov)
			apiKey := extractRateLimitAPIKey(r)
			model := ""

			estTokens, parsedModel := providers.EstimateRequestTokens(r, estCfg, prov)
			if parsedModel != "" {
				model = parsedModel
			}

			scope := ratelimit.ScopeKeys{Provider: prov.GetName(), Model: model, APIKey: apiKey, UserID: userID}
			res, err := limiter.CheckAndReserve(r.Context(), newReservationID(), scope, estTokens, time.Now())
			if err != nil {
				log.Printf("ratelimit: error reserving: %v", err)
				http.Error(w, "rate limit error", http.StatusInternalServerError)
				return
			}
			if !res.Allowed {
				w.Header().Set("Retry-After", fmtInt(res.RetryAfterSeconds))
				// Standard-ish headers inspired by GitHub/Stripe style and custom reason
				if res.Details != nil {
					// Metric and window
					w.Header().Set("X-RateLimit-Reason", res.Reason)
					w.Header().Set("X-RateLimit-Metric", res.Details.Metric)  // "requests" or "tokens"
					w.Header().Set("X-RateLimit-Window", res.Details.Window)  // "minute" or "day"
					w.Header().Set("X-RateLimit-Scope", res.Details.ScopeKey) // e.g. user:123, key:abc, model:..., provider:..., global
					w.Header().Set("X-RateLimit-Limit", fmtInt(res.Details.Limit))
					w.Header().Set("X-RateLimit-Remaining", fmtInt(res.Details.Remaining))
				}
				if stats != nil {
					metric, window, scopeKey := "", "", ""
					limit, remaining := 0, 0
					if res.Details != nil {
						metric = res.Details.Metric
						window = res.Details.Window
						scopeKey = res.Details.ScopeKey
						limit = res.Details.Limit
						remaining = res.Details.Remaining
					}
					stats.RecordDecision(
						prov.GetName(), model, apiKey, userID,
						false, res.Reason, metric, window, scopeKey, limit, remaining,
					)
				}
				log.Printf("ratelimit: throttle provider=%s model=%s user=%s key_prefix=%s reason=%s",
					prov.GetName(), model, userID, prefix(apiKey), res.Reason)
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}

			if stats != nil {
				stats.RecordDecision(prov.GetName(), model, apiKey, userID, true, "", "", "", "", 0, 0)
			}

			log.Printf("ratelimit: allow provider=%s model=%s user=%s key_prefix=%s est_tokens=%d",
				prov.GetName(), model, userID, prefix(apiKey), estTokens)

			// Proceed to next middleware/handler; TokenParsingMiddleware later
			// in the chain will set X-LLM-Total-Tokens if available.
			next.ServeHTTP(w, r)

			// Reconcile using input token metadata set by token parsing middleware headers if present
			actualInput := headerToInt(w.Header().Get("X-LLM-Input-Tokens"))
			if actualInput > 0 {
				delta := actualInput - estTokens
				if err := limiter.Adjust(r.Context(), "", scope, delta, time.Now()); err != nil {
					log.Printf("ratelimit: adjust error: %v", err)
				} else if delta != 0 {
					log.Printf("ratelimit: adjust (input) provider=%s model=%s user=%s key_prefix=%s delta_input_tokens=%d",
						prov.GetName(), model, userID, prefix(apiKey), delta)
				}
			}
		})
	}
}

func fmtInt(v int) string { return strconv.FormatInt(int64(v), 10) }

func headerToInt(s string) int {
	if s == "" {
		return 0
	}
	v, _ := strconv.Atoi(s)
	return v
}

// newReservationID returns a 128-bit random identifier (hex-encoded). The
// previous implementation returned the empty string, which made the
// reservation ID indistinguishable between requests and prevented future
// per-reservation tracking (e.g. Cancel correlation, metrics). We use
// crypto/rand rather than a UUID dependency since the value is opaque to
// every caller.
func newReservationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure on Linux/Darwin is essentially impossible; if
		// it happens, fall back to a timestamp-based identifier so the
		// process can keep serving rather than crashing under load.
		return "ts-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

func prefix(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// extractRateLimitAPIKey returns the iw: proxy key for rate-limit scoping.
// Prefer the record stashed by APIKeyValidationMiddleware (headers may already
// hold the translated upstream credential). Fall back to reading inbound auth
// headers for passthrough keys or when key management is disabled.
func extractRateLimitAPIKey(r *http.Request) string {
	if rec, ok := apikeys.FromContext(r.Context()); ok && rec != nil {
		return rec.PK
	}
	return extractInboundProxyKey(r)
}
