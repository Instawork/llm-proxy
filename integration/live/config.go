package live

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds runtime settings for live integration checks against a running
// llm-proxy instance. Everything is loaded from flags/env — no imports from the
// proxy codebase.
type Config struct {
	BaseURL      string
	PresidioURL  string
	CostFile     string
	SnippetsDir  string
	ModuleRoot   string
	Timeout      time.Duration
	Verbose      bool
	Suites       map[string]bool
	OpenAIKey    string
	AnthropicKey string
	GeminiKey    string
}

func LoadConfig(baseURL, presidioURL, costFile, snippetsDir, suiteList string, timeoutSec int, verbose bool) Config {
	cfg := Config{
		BaseURL:      strings.TrimRight(envOr("PROXY_BASE_URL", baseURL), "/"),
		PresidioURL:  strings.TrimRight(envOr("PRESIDIO_ANALYZER_URL", presidioURL), "/"),
		CostFile:     envOr("COST_TRACKING_FILE", costFile),
		Timeout:      time.Duration(timeoutSec) * time.Second,
		Verbose:      verbose,
		OpenAIKey:    os.Getenv("OPENAI_API_KEY"),
		AnthropicKey: os.Getenv("ANTHROPIC_API_KEY"),
		GeminiKey:    os.Getenv("GEMINI_API_KEY"),
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	if cfg.CostFile == "" {
		cfg.CostFile = "../logs/cost-tracking.jsonl"
	}
	dir := envOr("LLM_PROXY_SNIPPETS_DIR", snippetsDir)
	if dir == "" {
		dir = "snippets"
	}
	cfg.SnippetsDir = resolveSnippetsDir(dir)
	cfg.ModuleRoot = filepath.Dir(cfg.SnippetsDir)
	cfg.Suites = parseSuites(suiteList)
	return cfg
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseSuites(raw string) map[string]bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return map[string]bool{
			"health": true, "admin": true,
			"openai": true, "anthropic": true, "gemini": true,
			"ratelimit": true, "cost": true, "pii": true, "presidio": true,
			"redact":   true,
			"snippets": true,
		}
	}
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part != "" {
			out[part] = true
		}
	}
	return out
}

func (c Config) Suite(name string) bool {
	return c.Suites[name]
}

func intFromEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
