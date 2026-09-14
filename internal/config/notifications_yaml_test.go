package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNotifications_ProvidersPerEnvironment(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	cases := []struct {
		env      string
		provider string
	}{
		{"dev.yml", "log"},
		{"fuzz.yml", "log"},
		{"staging.yml", "sendgrid"},
		{"production.yml", "sendgrid"},
	}
	for _, c := range cases {
		cfg, err := LoadAndMergeConfigs([]string{
			filepath.Join(configsDir, "base.yml"),
			filepath.Join(configsDir, c.env),
		})
		if err != nil {
			t.Fatalf("load %s: %v", c.env, err)
		}
		if !cfg.Features.Notifications.Enabled {
			t.Errorf("%s: notifications.enabled must be true", c.env)
		}
		if got := cfg.Features.Notifications.Email.Provider; got != c.provider {
			t.Errorf("%s: notifications.email.provider = %q, want %q", c.env, got, c.provider)
		}
		if cfg.Features.Notifications.Email.FromAddress == "" {
			t.Errorf("%s: notifications.email.from_address must be set", c.env)
		}
	}
}
