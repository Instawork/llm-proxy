package apikeys

import "github.com/Instawork/llm-proxy/internal/config"

// RateLimitOverrides returns per-key rate-limit overrides stored on an API key
// record. ok is false when no override window is configured (all zeros).
func RateLimitOverrides(rec *APIKey) (config.LimitsConfig, bool) {
	if rec == nil {
		return config.LimitsConfig{}, false
	}
	lc := config.LimitsConfig{
		RequestsPerMinute: rec.RateLimitRPM,
		TokensPerMinute:   rec.RateLimitTPM,
		RequestsPerDay:    rec.RateLimitRPD,
		TokensPerDay:      rec.RateLimitTPD,
	}
	if lc.RequestsPerMinute > 0 || lc.TokensPerMinute > 0 || lc.RequestsPerDay > 0 || lc.TokensPerDay > 0 {
		return lc, true
	}
	return config.LimitsConfig{}, false
}
