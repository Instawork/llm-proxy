package config

import "testing"

func TestLookupRetiredModel_Alias(t *testing.T) {
	cfg := &YAMLConfig{
		RetiredModels: map[string]map[string]RetiredModelEntry{
			"gemini": {
				"gemini-2.0-flash": {
					RetiredDate: "2026-06-01",
					Replacement: "gemini-3.5-flash",
					Aliases:     []string{"gemini-flash-2.0", "gemini-2.0-flash-exp"},
				},
			},
		},
	}

	entry, ok := cfg.LookupRetiredModel("gemini", "gemini-2.0-flash-exp")
	if !ok {
		t.Fatal("expected alias match")
	}
	if entry.Replacement != "gemini-3.5-flash" {
		t.Fatalf("replacement=%q", entry.Replacement)
	}

	if _, ok := cfg.LookupRetiredModel("gemini", "gemini-2.5-flash"); ok {
		t.Fatal("unexpected match")
	}
}

func TestValidateRetiredModels_DuplicateAlias(t *testing.T) {
	cfg := &YAMLConfig{
		RetiredModels: map[string]map[string]RetiredModelEntry{
			"openai": {
				"o1-mini": {
					RetiredDate: "2025-10-27",
					Replacement: "o4-mini",
					Aliases:     []string{"o1-preview"},
				},
				"o1-preview": {
					RetiredDate: "2025-07-28",
					Replacement: "o3",
				},
			},
		},
	}
	if err := cfg.validateRetiredModels(); err == nil {
		t.Fatal("expected duplicate slug error")
	}
}
