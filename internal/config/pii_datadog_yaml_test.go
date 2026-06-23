package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPIIRedact_ProdHasDatadogMetrics(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	cfg, err := LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "production.yml"),
	})
	if err != nil {
		t.Fatalf("load production: %v", err)
	}
	if cfg.Features.PIIRedact.Datadog == nil {
		t.Fatal("production pii_redact.datadog must be configured")
	}
	if cfg.Features.IDGate.Datadog == nil {
		t.Fatal("production id_gate.datadog must be configured")
	}
}
