package config

import (
	"testing"
	"time"
)

func TestDefaultResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	t.Run("nil config uses package default", func(t *testing.T) {
		t.Parallel()
		var cfg *YAMLConfig
		if got := cfg.DefaultResponseHeaderTimeout(); got != DefaultResponseHeaderTimeoutSeconds*time.Second {
			t.Fatalf("got %v, want %ds", got, DefaultResponseHeaderTimeoutSeconds)
		}
	})

	t.Run("zero upstream uses package default", func(t *testing.T) {
		t.Parallel()
		cfg := &YAMLConfig{}
		if got := cfg.DefaultResponseHeaderTimeout(); got != 5*time.Minute {
			t.Fatalf("got %v, want 5m", got)
		}
	})

	t.Run("explicit upstream default", func(t *testing.T) {
		t.Parallel()
		cfg := &YAMLConfig{}
		cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = 120
		if got := cfg.DefaultResponseHeaderTimeout(); got != 2*time.Minute {
			t.Fatalf("got %v, want 2m", got)
		}
	})
}

func TestResponseHeaderTimeoutFor(t *testing.T) {
	t.Parallel()

	cfg := &YAMLConfig{
		Providers: map[string]ProviderConfig{
			"openai": {Enabled: true},
			"gemini": {Enabled: true, ResponseHeaderTimeoutSeconds: 600},
		},
	}
	cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = 300

	if got := cfg.ResponseHeaderTimeoutFor("openai"); got != 5*time.Minute {
		t.Fatalf("openai: got %v, want 5m (global default)", got)
	}
	if got := cfg.ResponseHeaderTimeoutFor("gemini"); got != 10*time.Minute {
		t.Fatalf("gemini: got %v, want 10m (provider override)", got)
	}
	if got := cfg.ResponseHeaderTimeoutFor("missing"); got != 5*time.Minute {
		t.Fatalf("missing provider: got %v, want 5m", got)
	}
}

func TestValidateUpstreamConfig(t *testing.T) {
	t.Parallel()

	cfg := GetDefaultYAMLConfig()
	cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = -1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative global timeout")
	}

	cfg = GetDefaultYAMLConfig()
	cfg.Providers["openai"] = ProviderConfig{
		Enabled:                      true,
		ResponseHeaderTimeoutSeconds: -5,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for negative provider timeout")
	}

	cfg = GetDefaultYAMLConfig()
	cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = 300
	cfg.Providers["openai"] = ProviderConfig{
		Enabled:                      true,
		ResponseHeaderTimeoutSeconds: 600,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validate error: %v", err)
	}
}
