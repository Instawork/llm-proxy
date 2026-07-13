package main

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
)

func TestBuildMantleModelProjects_ExpandsEnvAndAliases(t *testing.T) {
	t.Setenv("LLM_PROXY_BEDROCK_MANTLE_OPENAI_PROJECT_ID", "proj_env123")

	cfg := config.ProviderConfig{
		Models: map[string]config.ModelConfig{
			"openai.gpt-5.5": {
				Aliases:   []string{"gpt-5.5-bedrock", ""},
				ProjectID: "${LLM_PROXY_BEDROCK_MANTLE_OPENAI_PROJECT_ID}",
			},
			"anthropic.claude-sonnet-5": {}, // no project id -> skipped
		},
	}

	projects := buildMantleModelProjects(cfg)

	if got := projects["openai.gpt-5.5"]; got != "proj_env123" {
		t.Fatalf("canonical model project = %q, want proj_env123", got)
	}
	if got := projects["gpt-5.5-bedrock"]; got != "proj_env123" {
		t.Fatalf("alias project = %q, want proj_env123", got)
	}
	if _, ok := projects[""]; ok {
		t.Fatal("empty alias must not be mapped")
	}
	if _, ok := projects["anthropic.claude-sonnet-5"]; ok {
		t.Fatal("model without project id must be skipped")
	}
}

func TestBuildMantleModelProjects_NilWhenNoneConfigured(t *testing.T) {
	cfg := config.ProviderConfig{
		Models: map[string]config.ModelConfig{
			"openai.gpt-5.5": {ProjectID: "${LLM_PROXY_UNSET_PROJECT_ID}"}, // expands to ""
			"openai.gpt-5.4": {},
		},
	}
	if got := buildMantleModelProjects(cfg); got != nil {
		t.Fatalf("expected nil map when no project ids resolve, got %v", got)
	}
}
