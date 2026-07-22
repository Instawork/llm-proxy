// Cost-tracking end-to-end integration tests.
//
// These run in CI as part of `make test-integration` (name pattern
// Test(OpenAI|Anthropic|Gemini)Integration) whenever the provider API keys
// are present. Unlike the per-provider passthrough tests, they exercise the
// SAME pipeline production uses to bill a request:
//
//	created sk-iw key (DynamoDB store) → APIKeyValidationMiddleware →
//	provider reverse proxy → TokenParsingMiddleware → cost callback →
//	CostTracker transports + per-key admin spend stats
//
// and then assert that a cost record with non-zero tokens AND non-zero USD
// was actually written and attributed to the created key. This is the
// regression net for the Opik outage, where Anthropic's OpenAI-compatibility
// endpoint (/v1/chat/completions) parsed to zero tokens and silently
// produced no cost records — while PII stats kept counting.
//
// The file lives in package providers_test (external) so it can import the
// middleware package without an import cycle.
package providers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

// recordSink is an in-memory cost.Transport capturing every written record.
type recordSink struct {
	mu      sync.Mutex
	records []cost.CostRecord
}

func (s *recordSink) WriteRecord(record *cost.CostRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, *record)
	return nil
}

func (s *recordSink) snapshot() []cost.CostRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]cost.CostRecord, len(s.records))
	copy(out, s.records)
	return out
}

type costTrackingEnv struct {
	server *httptest.Server
	store  *apikeys.Store
	sink   *recordSink
	stats  *coststats.Recorder
}

// newCostTrackingEnv assembles the production cost-tracking pipeline against
// real upstream providers: key store backed by the shared DynamoDB fake,
// pricing loaded from the real configs/base.yml, and the exact middleware
// order used in cmd/llm-proxy/main.go runServer.
func newCostTrackingEnv(t *testing.T) *costTrackingEnv {
	t.Helper()

	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName:       "cost-tracking-integration-keys",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	pm.RegisterProvider(providers.NewAnthropicProxy())
	pm.RegisterProvider(providers.NewGeminiProxy())

	sink := &recordSink{}
	tracker := cost.NewCostTracker(sink)
	stats := coststats.NewRecorder()
	tracker.SetStatsRecorder(stats)
	loadPricingFromBaseConfig(t, tracker)

	// Mirrors the costTrackingCallback in cmd/llm-proxy/main.go, including
	// the TotalTokens > 0 gate: if response parsing regresses to zero
	// tokens (the Opik failure mode), no record is written and the test
	// fails — exactly the signal we want.
	costCallback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		if metadata.TotalTokens > 0 {
			provider := middleware.GetProviderFromRequest(pm, r)
			userID := middleware.ExtractUserIDFromRequest(r, provider)
			ipAddress := middleware.ExtractIPAddressFromRequest(r)
			keyID := ""
			if keyRecord, ok := apikeys.FromContext(r.Context()); ok && keyRecord != nil {
				keyID = middleware.MaskKeyID(keyRecord.PK)
			}
			if err := tracker.TrackRequest(metadata, userID, ipAddress, r.URL.Path, keyID); err != nil {
				t.Logf("cost tracking failed: %v", err)
			}
		}
	}

	r := mux.NewRouter()
	r.Use(middleware.APIKeyValidationMiddleware(pm, store, false, false))
	r.Use(middleware.TokenParsingMiddleware(pm, costCallback))
	for name, provider := range pm.GetAllProviders() {
		r.PathPrefix("/" + name + "/").Handler(provider.Proxy())
	}

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	return &costTrackingEnv{server: server, store: store, sink: sink, stats: stats}
}

// loadPricingFromBaseConfig mirrors initializeCostTracker in
// cmd/llm-proxy/main.go so the assertion "TotalCost > 0" also verifies that
// the models used by these tests still have pricing configured.
func loadPricingFromBaseConfig(t *testing.T, tracker *cost.CostTracker) {
	t.Helper()
	yamlConfig, err := config.LoadYAMLConfig(filepath.Join("..", "..", "configs", "base.yml"))
	require.NoError(t, err)

	total := 0
	for providerName, providerConfig := range yamlConfig.Providers {
		for modelName, modelConfig := range providerConfig.Models {
			if !modelConfig.Enabled || modelConfig.Pricing == nil {
				continue
			}
			modelPricing, ok := modelConfig.Pricing.(*config.ModelPricing)
			if !ok {
				continue
			}
			var trackerPricing cost.ModelPricing
			for _, tier := range modelPricing.Tiers {
				trackerPricing.Tiers = append(trackerPricing.Tiers, cost.PricingTier{
					Threshold: tier.Threshold,
					Input:     tier.Input,
					Output:    tier.Output,
				})
			}
			tracker.SetPricingForModel(providerName, modelName, &trackerPricing)
			total++
			for _, alias := range modelConfig.Aliases {
				tracker.SetPricingForModel(providerName, alias, &trackerPricing)
			}
		}
	}
	require.Greater(t, total, 0, "no pricing loaded from configs/base.yml — wrong working directory?")
}

func (env *costTrackingEnv) createKey(t *testing.T, provider, upstreamKey string) *apikeys.APIKey {
	t.Helper()
	created, err := env.store.CreateKey(
		context.Background(), provider, upstreamKey, "cost-tracking integration test", 0, nil, nil,
	)
	require.NoError(t, err)
	return created
}

// doJSON posts body to path with the given headers and requires HTTP 200,
// returning the response body for debugging context on failures.
func (env *costTrackingEnv) doJSON(t *testing.T, path string, headers map[string]string, body map[string]interface{}) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequest(http.MethodPost, env.server.URL+path, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode == http.StatusTooManyRequests {
		// Upstream quota blip (shared CI provider keys) — not a cost-tracking
		// regression. Match the wider integration suite's convention of
		// skipping on missing prerequisites rather than failing the build.
		t.Skipf("upstream rate limited (429): %s", string(respBody))
	}
	require.Equal(t, http.StatusOK, resp.StatusCode, "upstream error: %s", string(respBody))
	return respBody
}

// requireCostTracked waits for a new cost record (past baseline) for the
// given provider and asserts the full billing contract: non-zero tokens,
// non-zero USD, and per-key spend attribution in the admin stats recorder.
func (env *costTrackingEnv) requireCostTracked(t *testing.T, baseline int, wantProvider, maskedKey string) cost.CostRecord {
	t.Helper()

	var got cost.CostRecord
	require.Eventually(t, func() bool {
		records := env.sink.snapshot()
		if len(records) <= baseline {
			return false
		}
		for _, record := range records[baseline:] {
			if record.Provider == wantProvider {
				got = record
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond,
		"no cost record written for provider %s — token parsing likely returned zero tokens", wantProvider)

	assert.Greater(t, got.InputTokens, 0, "input tokens missing from cost record")
	// Gemini OpenAI-compat (and other thinking models) can report completion_tokens=0
	// when the max_tokens budget is consumed by thought tokens, while still setting
	// total_tokens > prompt_tokens. Treat that gap as billable output for this gate.
	assert.True(t, got.OutputTokens > 0 || got.TotalTokens > got.InputTokens,
		"output tokens missing from cost record (output=%d total=%d input=%d)",
		got.OutputTokens, got.TotalTokens, got.InputTokens)
	assert.Greater(t, got.TotalCost, 0.0,
		"cost record has zero USD — no pricing matched model %q for provider %s", got.Model, wantProvider)

	keySpend := env.stats.KeySpendUSD(context.Background(), maskedKey)
	assert.Greater(t, keySpend, 0.0, "admin spend stats missing attribution for key %s", maskedKey)

	return got
}

func (env *costTrackingEnv) recordCount() int {
	return len(env.sink.snapshot())
}

// ----------------------------------------------------------------------------
// OpenAI (native /chat/completions — the baseline that always worked)
// ----------------------------------------------------------------------------

func TestOpenAIIntegration_CostTracking_CreatedKey(t *testing.T) {
	upstreamKey := os.Getenv("OPENAI_API_KEY")
	if upstreamKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}

	env := newCostTrackingEnv(t)
	created := env.createKey(t, "openai", upstreamKey)
	masked := middleware.MaskKeyID(created.PK)

	baseline := env.recordCount()
	env.doJSON(t, "/openai/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + created.PK},
		map[string]interface{}{
			"model":      "gpt-4o-mini",
			"max_tokens": 32,
			"messages":   []map[string]string{{"role": "user", "content": "Say hi in one word."}},
		})

	record := env.requireCostTracked(t, baseline, "openai", masked)
	assert.Contains(t, record.Model, "gpt-4o-mini")
}

// ----------------------------------------------------------------------------
// Anthropic: native /v1/messages and the OpenAI-compatibility endpoint
// /v1/chat/completions (the Opik regression).
// ----------------------------------------------------------------------------

func TestAnthropicIntegration_CostTracking_CreatedKey(t *testing.T) {
	upstreamKey := os.Getenv("ANTHROPIC_API_KEY")
	if upstreamKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	env := newCostTrackingEnv(t)
	created := env.createKey(t, "anthropic", upstreamKey)
	masked := middleware.MaskKeyID(created.PK)

	baseline := env.recordCount()
	env.doJSON(t, "/anthropic/v1/messages",
		map[string]string{"x-api-key": created.PK, "anthropic-version": "2023-06-01"},
		map[string]interface{}{
			"model":      "claude-haiku-4-5",
			"max_tokens": 32,
			"messages":   []map[string]string{{"role": "user", "content": "Say hi in one word."}},
		})

	record := env.requireCostTracked(t, baseline, "anthropic", masked)
	assert.Contains(t, record.Model, "claude-haiku-4-5")
}

func TestAnthropicIntegration_CostTracking_CreatedKey_OpenAICompat(t *testing.T) {
	upstreamKey := os.Getenv("ANTHROPIC_API_KEY")
	if upstreamKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	env := newCostTrackingEnv(t)
	created := env.createKey(t, "anthropic", upstreamKey)
	masked := middleware.MaskKeyID(created.PK)
	headers := map[string]string{"Authorization": "Bearer " + created.PK}

	t.Run("NonStreaming", func(t *testing.T) {
		baseline := env.recordCount()
		env.doJSON(t, "/anthropic/v1/chat/completions", headers,
			map[string]interface{}{
				"model":      "claude-haiku-4-5",
				"max_tokens": 32,
				"messages":   []map[string]string{{"role": "user", "content": "Say hi in one word."}},
			})

		record := env.requireCostTracked(t, baseline, "anthropic", masked)
		assert.Contains(t, record.Model, "claude-haiku-4-5")
	})

	t.Run("Streaming", func(t *testing.T) {
		baseline := env.recordCount()
		env.doJSON(t, "/anthropic/v1/chat/completions", headers,
			map[string]interface{}{
				"model":          "claude-haiku-4-5",
				"max_tokens":     32,
				"stream":         true,
				"stream_options": map[string]bool{"include_usage": true},
				"messages":       []map[string]string{{"role": "user", "content": "Say hi in one word."}},
			})

		record := env.requireCostTracked(t, baseline, "anthropic", masked)
		assert.True(t, record.IsStreaming, "streaming request should produce a streaming cost record")
	})
}

// ----------------------------------------------------------------------------
// Gemini: native :generateContent and the OpenAI-compatibility endpoint
// /v1beta/openai/chat/completions.
// ----------------------------------------------------------------------------

func TestGeminiIntegration_CostTracking_CreatedKey(t *testing.T) {
	upstreamKey := os.Getenv("GEMINI_API_KEY")
	if upstreamKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	env := newCostTrackingEnv(t)
	created := env.createKey(t, "gemini", upstreamKey)
	masked := middleware.MaskKeyID(created.PK)

	baseline := env.recordCount()
	env.doJSON(t, "/gemini/v1beta/models/gemini-2.5-flash:generateContent",
		map[string]string{"x-goog-api-key": created.PK},
		map[string]interface{}{
			"contents": []map[string]interface{}{
				{"parts": []map[string]string{{"text": "Say hi in one word."}}},
			},
			"generationConfig": map[string]interface{}{"maxOutputTokens": 32},
		})

	record := env.requireCostTracked(t, baseline, "gemini", masked)
	assert.Contains(t, record.Model, "gemini-2.5-flash")
}

func TestGeminiIntegration_CostTracking_CreatedKey_OpenAICompat(t *testing.T) {
	upstreamKey := os.Getenv("GEMINI_API_KEY")
	if upstreamKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	env := newCostTrackingEnv(t)
	created := env.createKey(t, "gemini", upstreamKey)
	masked := middleware.MaskKeyID(created.PK)
	headers := map[string]string{"Authorization": "Bearer " + created.PK}

	// gemini-2.5-flash thinking can consume a small max_tokens budget entirely,
	// leaving completion_tokens=0 (and sometimes an empty stream with no usage
	// chunk). Leave headroom so the OpenAI-compat path still emits visible
	// completion tokens + include_usage for cost tracking.
	const geminiCompatMaxTokens = 256

	t.Run("NonStreaming", func(t *testing.T) {
		baseline := env.recordCount()
		env.doJSON(t, "/gemini/v1beta/openai/chat/completions", headers,
			map[string]interface{}{
				"model":      "gemini-2.5-flash",
				"max_tokens": geminiCompatMaxTokens,
				"messages":   []map[string]string{{"role": "user", "content": "Say hi in one word."}},
			})

		record := env.requireCostTracked(t, baseline, "gemini", masked)
		assert.Contains(t, record.Model, "gemini-2.5-flash")
	})

	t.Run("Streaming", func(t *testing.T) {
		baseline := env.recordCount()
		env.doJSON(t, "/gemini/v1beta/openai/chat/completions", headers,
			map[string]interface{}{
				"model":          "gemini-2.5-flash",
				"max_tokens":     geminiCompatMaxTokens,
				"stream":         true,
				"stream_options": map[string]bool{"include_usage": true},
				"messages":       []map[string]string{{"role": "user", "content": "Say hi in one word."}},
			})

		record := env.requireCostTracked(t, baseline, "gemini", masked)
		assert.True(t, record.IsStreaming, "streaming request should produce a streaming cost record")
	})
}
