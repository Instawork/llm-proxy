package main

import (
	"testing"
)

func TestSummarize(t *testing.T) {
	counts := []pathCount{
		{"/health", 49865},
		{"/gemini/v1beta/interactions", 23357},
		{"/gemini/v1beta/models/gemini-2.5-flash:generateContent", 296},
		{"/gemini/v1beta/models/gemini-3.7-flash:generateContent", 57},
		{"/meta/Archivist/gemini/v1beta/models/gemini-3.5-flash:generateContent", 6},
		{"/gemini/upload/v1beta/files", 284},
		{"/bedrock/foundation-models", 15},
		{"/bedrock/model/anthropic.claude-3-haiku-20240307-v1:0/invoke", 33},
		{"/bedrock/..%2fadmin/api/keys", 1},
		{"/bedrock/@169.254.169.254/latest/meta-data/", 1},
		{"/admin/api/cost", 997},
	}

	rows, noise := summarize(counts)
	if noise != 2 {
		t.Fatalf("noise = %d, want 2", noise)
	}

	want := []row{
		{"unknown", "/bedrock/foundation-models", 15},
		{"passthrough", "/gemini/upload/v1beta/files", 284},
		{"metered", "/gemini/v1beta/interactions", 23357},
		{"metered", "/gemini/v1beta/models/{model}:generateContent", 353},
		{"metered", "/bedrock/model/{model}/invoke", 33},
		{"metered", "/meta/{name}/gemini/v1beta/models/{model}:generateContent", 6},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
}

func TestIsProviderPath(t *testing.T) {
	cases := map[string]bool{
		"/openai/v1/responses":                true,
		"/meta/Partner%20Home/openai/v1/chat": true,
		"/bedrock-mantle/v1/messages":         true,
		"/model/anthropic.claude/invoke":      true,
		"/admin/api/keys":                     false,
		"/health":                             false,
		"/meta/only-name":                     false,
	}
	for path, want := range cases {
		if got := isProviderPath(path); got != want {
			t.Errorf("isProviderPath(%q) = %v, want %v", path, got, want)
		}
	}
}
