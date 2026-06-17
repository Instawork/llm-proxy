package main

import (
	"os"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
)

func TestIsFakeModeAllowed_RequiresEnv(t *testing.T) {
	yc := config.GetDefaultYAMLConfig()
	yc.Features.FakeUpstream.Enabled = true
	os.Unsetenv("LLM_PROXY_ALLOW_FAKE_MODE")
	if isFakeModeAllowed(yc) {
		t.Fatal("expected false without env")
	}
}

func TestIsFakeModeAllowed_RequiresYAML(t *testing.T) {
	yc := config.GetDefaultYAMLConfig()
	yc.Features.FakeUpstream.Enabled = false
	t.Setenv("LLM_PROXY_ALLOW_FAKE_MODE", "1")
	if isFakeModeAllowed(yc) {
		t.Fatal("expected false when yaml disabled")
	}
}

func TestIsFakeModeAllowed_BothRequired(t *testing.T) {
	yc := config.GetDefaultYAMLConfig()
	yc.Features.FakeUpstream.Enabled = true
	t.Setenv("LLM_PROXY_ALLOW_FAKE_MODE", "1")
	if !isFakeModeAllowed(yc) {
		t.Fatal("expected true when both gates satisfied")
	}
}
