package admin

import (
	"log/slog"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
)

// Deps bundles dependencies for the admin dashboard API.
type Deps struct {
	Logger           *slog.Logger
	YAMLConfig       *config.YAMLConfig
	APIKeyStore      *apikeys.Store
	APIKeyStoreError error
	UserStore        *adminusers.Store
	UserStoreError   error
	AdminRollupStore *adminrollup.Store
	RateLimiter      ratelimit.RateLimiter
	HealthFunc       http.HandlerFunc
	CostSummary      func() map[string]interface{}
	PIISummary       func() map[string]interface{}
	UsageSummary     func() map[string]interface{}
	RateLimitSummary func() map[string]interface{}
	CircuitActivity  func() map[string]interface{}
	KeyProvisioner   *provision.Manager
}
