package admin

import (
	"log/slog"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
)

// Deps bundles dependencies for the admin dashboard API.
type Deps struct {
	Logger           *slog.Logger
	YAMLConfig       *config.YAMLConfig
	APIKeyStore      *apikeys.Store
	APIKeyStoreError error
	RateLimiter      ratelimit.RateLimiter
	HealthFunc       http.HandlerFunc
	CostSummary      func() map[string]interface{}
	PIISummary       func() map[string]interface{}
	UsageSummary     func() map[string]interface{}
	RateLimitSummary func() map[string]interface{}
}
