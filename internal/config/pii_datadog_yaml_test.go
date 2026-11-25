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
	if cfg.Features.IDGate.TimeoutMs < 15000 {
		t.Fatalf("production id_gate.timeout_ms = %d, want >= 15000", cfg.Features.IDGate.TimeoutMs)
	}
	if cfg.Features.IDGate.MaxInFlight <= 0 {
		t.Fatalf("production id_gate.max_in_flight = %d, want > 0", cfg.Features.IDGate.MaxInFlight)
	}
	if cfg.Features.IDGate.MaxImageDimension <= 0 {
		t.Fatalf("production id_gate.max_image_dimension = %d, want > 0", cfg.Features.IDGate.MaxImageDimension)
	}
	if cfg.Features.IDGate.CacheTTLSeconds <= 0 {
		t.Fatalf("production id_gate.cache_ttl_seconds = %d, want > 0", cfg.Features.IDGate.CacheTTLSeconds)
	}
}
