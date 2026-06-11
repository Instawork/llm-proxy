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
	Logger      *slog.Logger
	YAMLConfig  *config.YAMLConfig
	APIKeyStore *apikeys.Store
	RateLimiter ratelimit.RateLimiter
	HealthFunc  http.HandlerFunc
	CostSummary func() map[string]interface{}
}
