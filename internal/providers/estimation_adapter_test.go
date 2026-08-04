package providers

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestNewYAMLConfigEstimationAdapter_ZeroValueFillsDefaults(t *testing.T) {
	a := NewYAMLConfigEstimationAdapter(config.EstimationConfig{})
	assert.Equal(t, defaultEstimationMaxSampleBytes, a.GetMaxSampleBytes())
	assert.Equal(t, defaultEstimationBytesPerToken, a.GetBytesPerToken())
	assert.Equal(t, defaultEstimationCharsPerToken, a.GetCharsPerToken())
}

func TestNewYAMLConfigEstimationAdapter_PreservesExplicitValues(t *testing.T) {
	a := NewYAMLConfigEstimationAdapter(config.EstimationConfig{
		MaxSampleBytes:        1024,
		BytesPerToken:         3,
		CharsPerToken:         5,
		ProviderCharsPerToken: map[string]int{"openai": 5},
	})
	assert.Equal(t, 1024, a.GetMaxSampleBytes())
	assert.Equal(t, 3, a.GetBytesPerToken())
	assert.Equal(t, 5, a.GetCharsPerToken())
	assert.Equal(t, 5, a.GetProviderCharsPerToken("openai"))
}

// TestNewYAMLConfigEstimationAdapter_MissingBlockStillExtractsModel is the
// regression test for the production bug: a wholly absent
// rate_limiting.estimation block (config.EstimationConfig{}, its YAML zero
// value) used to leave MaxSampleBytes at 0, which made
// EstimateRequestTokens treat every non-empty request body as too large to
// sample and never extract a model name. Building the adapter through the
// shared constructor must keep model extraction working even with no
// estimation config at all.
func TestNewYAMLConfigEstimationAdapter_MissingBlockStillExtractsModel(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`)
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")

	adapter := NewYAMLConfigEstimationAdapter(config.EstimationConfig{})
	n, model := EstimateRequestTokens(req, adapter, NewOpenAIProxy())
	assert.Equal(t, "gpt-4o", model)
	assert.Greater(t, n, 0)
}
