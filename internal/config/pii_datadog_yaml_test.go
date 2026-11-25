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

func TestIDGate_ProdConfig(t *testing.T) {
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
	g := cfg.Features.IDGate
	if g.TimeoutMs < 15000 {
		t.Fatalf("production id_gate.timeout_ms = %d, want >= 15000", g.TimeoutMs)
	}
	if g.QueueTimeoutMs <= 0 || g.QueueTimeoutMs > 10000 {
		t.Fatalf("production id_gate.queue_timeout_ms = %d, want in (0, 10000]", g.QueueTimeoutMs)
	}
	if g.MaxInFlight <= 0 || g.MaxInFlight > 8 {
		t.Fatalf("production id_gate.max_in_flight = %d, want in (0, 8]", g.MaxInFlight)
	}
	if g.CacheTTLSeconds == nil || *g.CacheTTLSeconds <= 0 {
		t.Fatalf("production id_gate.cache_ttl_seconds = %v, want > 0", g.CacheTTLSeconds)
	}
}

func TestIDGate_OverlayCanDisableVerdictCache(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	overlayPath := filepath.Join(t.TempDir(), "disable-cache.yml")
	overlay := "features:\n  id_gate:\n    cache_ttl_seconds: 0\n"
	if err := os.WriteFile(overlayPath, []byte(overlay), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}

	cfg, err := LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "production.yml"),
		overlayPath,
	})
	if err != nil {
		t.Fatalf("load with cache-disable overlay: %v", err)
	}
	if cfg.Features.IDGate.CacheTTLSeconds == nil || *cfg.Features.IDGate.CacheTTLSeconds != 0 {
		t.Fatalf("cache_ttl_seconds = %v, want explicit 0 after overlay", cfg.Features.IDGate.CacheTTLSeconds)
	}
}
