package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// TestCircuitModelExtractor_DispatchesToRealProviders pins the
// production wiring contract: circuitModelExtractor must dispatch by
// URL prefix to the right provider's body parser, so the model name
// shows up on circuit-breaker log lines and metric tags for OpenAI,
// Anthropic, and Gemini requests alike.
//
// The fakeModelFn used in internal/circuit tests proves the
// transport plumbs *something* through, but only this end-to-end
// fixture proves the real extractors are reachable from the circuit
// path.  Without it a refactor of providers.OpenAIProxy could
// silently break model attribution in production while every circuit
// test still passes.
func TestCircuitModelExtractor_DispatchesToRealProviders(t *testing.T) {
	openAIProvider := providers.NewOpenAIProxy()
	anthropicProvider := providers.NewAnthropicProxy()
	geminiProvider := providers.NewGeminiProxy()
	bedrockProvider := providers.NewBedrockProxy()

	extract := circuitModelExtractor(openAIProvider, anthropicProvider, geminiProvider, bedrockProvider)

	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{
			name: "openai chat completions",
			path: "/openai/v1/chat/completions",
			body: `{"model":"gpt-4o-mini","messages":[]}`,
			want: "gpt-4o-mini",
		},
		{
			name: "openai chat completions after reverse-proxy director stripped provider prefix",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-4o","messages":[]}`,
			want: "gpt-4o",
		},
		{
			name: "openai responses api",
			path: "/openai/v1/responses",
			body: `{"model":"o3-mini","input":"hello"}`,
			want: "o3-mini",
		},
		{
			name: "anthropic messages",
			path: "/anthropic/v1/messages",
			body: `{"model":"claude-3-5-sonnet-20240620","messages":[]}`,
			want: "claude-3-5-sonnet-20240620",
		},
		{
			name: "anthropic messages after reverse-proxy director stripped provider prefix",
			path: "/v1/messages",
			body: `{"model":"claude-3-5-sonnet-latest","messages":[]}`,
			want: "claude-3-5-sonnet-latest",
		},
		{
			name: "gemini generateContent (URL-derived model)",
			path: "/gemini/v1beta/models/gemini-2.5-flash:generateContent",
			body: `{"contents":[]}`,
			want: "gemini-2.5-flash",
		},
		{
			name: "bedrock converse (URL-derived model)",
			path: "/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
			body: `{"messages":[]}`,
			want: "us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		},
		{
			name: "non-matching path returns empty",
			path: "/healthz",
			body: ``,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			}
			var req *http.Request
			if bodyReader != nil {
				req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, bodyReader)
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequestWithContext(context.Background(), http.MethodPost, tc.path, http.NoBody)
			}
			if got := extract(req); got != tc.want {
				t.Fatalf("circuitModelExtractor(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestCircuitModelExtractor_NilSafe verifies that the extractor never
// panics on degenerate inputs.  A misrouted request that somehow
// reaches the circuit transport with a nil URL must produce "" and
// not crash, since model attribution is best-effort.
func TestCircuitModelExtractor_NilSafe(t *testing.T) {
	extract := circuitModelExtractor(
		providers.NewOpenAIProxy(),
		providers.NewAnthropicProxy(),
		providers.NewGeminiProxy(),
		providers.NewBedrockProxy(),
	)
	if got := extract(nil); got != "" {
		t.Fatalf("nil request: want \"\", got %q", got)
	}
}

// TestCircuitModelExtractor_NilBedrockSafe ensures that when Bedrock is
// disabled (provider is nil) the extractor still returns "" for any
// `/bedrock/...` path that somehow reaches it, rather than panicking.
func TestCircuitModelExtractor_NilBedrockSafe(t *testing.T) {
	extract := circuitModelExtractor(
		providers.NewOpenAIProxy(),
		providers.NewAnthropicProxy(),
		providers.NewGeminiProxy(),
		nil,
	)
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		http.NoBody,
	)
	if got := extract(req); got != "" {
		t.Fatalf("disabled bedrock: want \"\", got %q", got)
	}
}

// TestCircuitDatadogConfig_PrefersFirstDatadogTransport ensures the
// circuit-metrics initialiser picks up the same Datadog block the
// cost tracker uses, so circuit metrics inherit env / service / team
// tags without operators having to declare them twice.  Also
// confirms it returns nil when no Datadog transport is present so
// circuit.NewTransport falls back to its noop sink.
func TestCircuitDatadogConfig_PrefersFirstDatadogTransport(t *testing.T) {
	yamlCfg := &config.YAMLConfig{
		Features: config.FeaturesConfig{
			CostTracking: config.CostTrackingConfig{
				Enabled: true,
				Transports: []config.TransportConfig{
					{Type: "dynamodb", DynamoDB: &config.DynamoDBTransportConfig{TableName: "x"}},
					{Type: "datadog", Datadog: &config.DatadogTransportConfig{
						Host: "127.0.0.1", Port: "9999",
						Namespace: "llm",
						Tags:      []string{"env:test"},
					}},
					{Type: "file", File: &config.FileTransportConfig{Path: "/tmp/x"}},
				},
			},
		},
	}
	got := circuitDatadogConfig(yamlCfg)
	if got == nil {
		t.Fatal("expected non-nil DatadogTransportConfig from cost-tracking section")
	}
	if got.Host != "127.0.0.1" || got.Port != "9999" || got.Namespace != "llm" {
		t.Fatalf("circuitDatadogConfig returned %+v, want host=127.0.0.1 port=9999 namespace=llm", got)
	}

	// Empty config → nil → "metrics disabled".
	emptyCfg := &config.YAMLConfig{}
	if circuitDatadogConfig(emptyCfg) != nil {
		t.Fatal("circuitDatadogConfig must return nil when no datadog transport is configured")
	}
}
