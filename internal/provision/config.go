package provision

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/internal/config"
	redis "github.com/redis/go-redis/v9"
)

// RuntimeConfig is resolved provisioning configuration (secrets from env).
type RuntimeConfig struct {
	Enabled bool

	OpenAIEnabled   bool
	OpenAIAdminKey  string
	OpenAIProjectID string

	GeminiEnabled      bool
	GCPProjectID       string
	GCPCredentialsJSON string

	AnthropicEnabled  bool
	AnthropicAdminKey string
	AnthropicPoolKey  string

	RedisURL string
}

// RuntimeFromYAML builds runtime config from YAML + environment.
func RuntimeFromYAML(cfg config.KeyProvisioningConfig) RuntimeConfig {
	rt := RuntimeConfig{
		Enabled:          cfg.Enabled,
		OpenAIEnabled:    cfg.OpenAI.Enabled,
		OpenAIProjectID:  os.ExpandEnv(cfg.OpenAI.ProjectID),
		GeminiEnabled:    cfg.Gemini.Enabled,
		GCPProjectID:     os.ExpandEnv(cfg.Gemini.GCPProjectID),
		AnthropicEnabled: cfg.Anthropic.Enabled,
		AnthropicPoolKey: cfg.Anthropic.PoolRedisKey,
	}
	if rt.AnthropicPoolKey == "" {
		rt.AnthropicPoolKey = "llm:provision:anthropic:available"
	}

	rt.OpenAIAdminKey = os.Getenv("LLM_PROXY_OPENAI_ADMIN_KEY")
	if v := os.Getenv("LLM_PROXY_OPENAI_PROJECT_ID"); v != "" {
		rt.OpenAIProjectID = v
	}
	rt.GCPCredentialsJSON = os.Getenv("LLM_PROXY_GCP_CREDENTIALS_JSON")
	if v := os.Getenv("LLM_PROXY_GCP_PROJECT_ID"); v != "" {
		rt.GCPProjectID = v
	}
	rt.AnthropicAdminKey = os.Getenv("LLM_PROXY_ANTHROPIC_ADMIN_KEY")
	rt.RedisURL = os.Getenv("REDIS_URL")

	return rt
}

// NewManagerFromRuntime constructs a Manager from resolved runtime config.
func NewManagerFromRuntime(rt RuntimeConfig, logger *slog.Logger) (*Manager, error) {
	if !rt.Enabled {
		return NewManager(nil, nil), nil
	}

	byProvider := map[string]Provisioner{}

	if rt.OpenAIEnabled && rt.OpenAIAdminKey != "" && rt.OpenAIProjectID != "" {
		byProvider["openai"] = NewOpenAI(rt.OpenAIAdminKey, rt.OpenAIProjectID, "")
	}

	if rt.GeminiEnabled && rt.GCPProjectID != "" && rt.GCPCredentialsJSON != "" {
		g, err := NewGemini(rt.GCPProjectID, []byte(rt.GCPCredentialsJSON), "")
		if err != nil {
			return nil, fmt.Errorf("gemini provisioner: %w", err)
		}
		byProvider["gemini"] = g
	}

	if rt.AnthropicEnabled && rt.RedisURL != "" {
		opts, err := redis.ParseURL(rt.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("anthropic pool redis url: %w", err)
		}
		client := redis.NewClient(opts)
		byProvider["anthropic"] = NewAnthropicPool(client, rt.AnthropicPoolKey, rt.AnthropicAdminKey)
	}

	return NewManager(logger, byProvider), nil
}

// SanitizeName makes a vendor-safe resource name from a description.
func SanitizeName(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return "llm-proxy-key"
	}
	var b strings.Builder
	b.Grow(len(description))
	for _, r := range description {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "llm-proxy-key"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
