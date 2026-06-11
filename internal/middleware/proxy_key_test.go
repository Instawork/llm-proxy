package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/stretchr/testify/assert"
)

func TestExtractInboundProxyKey(t *testing.T) {
	iwKey := apikeys.KeyPrefix + "abc123"

	t.Run("bearer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
		req.Header.Set("Authorization", "Bearer "+iwKey)
		assert.Equal(t, iwKey, extractInboundProxyKey(req))
	})

	t.Run("x-api-key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
		req.Header.Set("x-api-key", iwKey)
		assert.Equal(t, iwKey, extractInboundProxyKey(req))
	})

	t.Run("x-goog-api-key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models", nil)
		req.Header.Set("x-goog-api-key", iwKey)
		assert.Equal(t, iwKey, extractInboundProxyKey(req))
	})

	t.Run("query key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/gemini/v1beta/models?key="+iwKey, nil)
		assert.Equal(t, iwKey, extractInboundProxyKey(req))
	})

	t.Run("empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
		assert.Equal(t, "", extractInboundProxyKey(req))
	})
}

func TestExtractRateLimitAPIKey_PrefersContext(t *testing.T) {
	iwKey := apikeys.KeyPrefix + "fromctx"
	record := &apikeys.APIKey{PK: iwKey}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-upstream-after-swap")
	req = req.WithContext(apikeys.WithContext(req.Context(), record))

	assert.Equal(t, iwKey, extractRateLimitAPIKey(req))
}

func TestExtractRateLimitAPIKey_FallsBackToInbound(t *testing.T) {
	iwKey := apikeys.KeyPrefix + "inbound"
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("x-api-key", iwKey)

	assert.Equal(t, iwKey, extractRateLimitAPIKey(req))
}
