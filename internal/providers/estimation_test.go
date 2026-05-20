package providers

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

type fakeEstCfg struct {
	maxSample           int
	bytesPerToken       int
	charsPerToken       int
	providerCharsByName map[string]int
}

func (f fakeEstCfg) GetMaxSampleBytes() int                   { return f.maxSample }
func (f fakeEstCfg) GetBytesPerToken() int                    { return f.bytesPerToken }
func (f fakeEstCfg) GetCharsPerToken() int                    { return f.charsPerToken }
func (f fakeEstCfg) GetProviderCharsPerToken(name string) int { return f.providerCharsByName[name] }

func TestEstimateRequestTokens_NilCfg(t *testing.T) {
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	n, m := EstimateRequestTokens(req, nil, NewOpenAIProxy())
	assert.Equal(t, 0, n)
	assert.Equal(t, "", m)
}

func TestEstimateRequestTokens_ContentLengthFallback(t *testing.T) {
	body := strings.NewReader("X")
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", body)
	req.ContentLength = 100
	req.Header.Set("Content-Type", "text/plain")
	n, _ := EstimateRequestTokens(req, fakeEstCfg{maxSample: -1, bytesPerToken: 4}, NewOpenAIProxy())
	assert.Equal(t, 25, n)
}

func TestEstimateRequestTokens_JSONMessageSampling_OpenAI(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`)
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	cfg := fakeEstCfg{maxSample: 1024, bytesPerToken: 4, charsPerToken: 4}
	n, model := EstimateRequestTokens(req, cfg, NewOpenAIProxy())
	assert.Equal(t, "gpt-4o", model)
	assert.Greater(t, n, 0)
}

func TestEstimateRequestTokens_BodyTooLarge_LogsAndUsesContentLength(t *testing.T) {
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(""))
	req.ContentLength = 5000
	req.Header.Set("Content-Type", "application/json")
	cfg := fakeEstCfg{maxSample: 256, bytesPerToken: 4}
	n, model := EstimateRequestTokens(req, cfg, NewOpenAIProxy())
	assert.Equal(t, "", model)
	assert.Equal(t, 1250, n)
}
