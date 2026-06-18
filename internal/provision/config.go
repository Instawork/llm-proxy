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
	DevFake bool

	OpenAIEnabled   bool
	OpenAIAdminKey  string
	OpenAIProjectID string

	GeminiEnabled      bool
	GCPProjectID       string
	GCPCredentialsJSON string

	AnthropicEnabled       bool
	AnthropicAdminKey      string
	AnthropicDefaultTier   string
	AnthropicTierKeys      map[string]string
	AnthropicTiersFromYAML bool
	AnthropicPoolKey       string

	RedisURL string
}

// RuntimeFromYAML builds runtime config from YAML + environment.
func RuntimeFromYAML(cfg config.KeyProvisioningConfig) RuntimeConfig {
	rt := RuntimeConfig{
		Enabled:              cfg.Enabled,
		DevFake:              cfg.DevFake,
		OpenAIEnabled:        cfg.OpenAI.Enabled,
		OpenAIProjectID:      os.ExpandEnv(cfg.OpenAI.ProjectID),
		GeminiEnabled:        cfg.Gemini.Enabled,
		GCPProjectID:         os.ExpandEnv(cfg.Gemini.GCPProjectID),
		AnthropicEnabled:     cfg.Anthropic.Enabled,
		AnthropicDefaultTier: cfg.Anthropic.DefaultTier,
		AnthropicPoolKey:     cfg.Anthropic.PoolRedisKey,
	}
	if rt.AnthropicDefaultTier == "" {
		rt.AnthropicDefaultTier = TierMetered
	}
	if rt.AnthropicPoolKey == "" {
		rt.AnthropicPoolKey = "llm:provision:anthropic:available"
	}

	rt.AnthropicTiersFromYAML = len(cfg.Anthropic.Tiers) > 0
	if rt.AnthropicTiersFromYAML {
		rt.AnthropicTierKeys = resolveAnthropicTierKeys(cfg.Anthropic.Tiers)
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

func resolveAnthropicTierKeys(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for tier, raw := range cfg {
		tier = strings.ToLower(strings.TrimSpace(tier))
		key := strings.TrimSpace(os.ExpandEnv(raw))
		if tier == "" || key == "" {
			continue
		}
		out[tier] = key
	}
	return out
}

// NewManagerFromRuntime constructs a Manager from resolved runtime config.
func NewManagerFromRuntime(rt RuntimeConfig, logger *slog.Logger) (*Manager, error) {
	if !rt.Enabled {
		return NewManager(nil, nil), nil
	}

	if rt.DevFake {
		byProvider := map[string]Provisioner{
			"openai":    NewFake("openai"),
			"anthropic": NewFakeAnthropic(),
			"gemini":    NewFake("gemini"),
		}
		return NewManager(logger, byProvider), nil
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

	if rt.AnthropicEnabled {
		switch {
		case rt.AnthropicTiersFromYAML || len(rt.AnthropicTierKeys) > 0:
			a, err := NewAnthropicTiered(rt.AnthropicDefaultTier, rt.AnthropicTierKeys)
			if err != nil {
				return nil, fmt.Errorf("anthropic tiered provisioner: %w", err)
			}
			byProvider["anthropic"] = a
		case rt.RedisURL != "":
			opts, err := redis.ParseURL(rt.RedisURL)
			if err != nil {
				return nil, fmt.Errorf("anthropic pool redis url: %w", err)
			}
			client := redis.NewClient(opts)
			byProvider["anthropic"] = NewAnthropicPool(client, rt.AnthropicPoolKey, rt.AnthropicAdminKey)
		}
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
