package providers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderManager_RegisterAndLookup(t *testing.T) {
	pm := NewProviderManager()
	pm.RegisterProvider(NewOpenAIProxy())
	pm.RegisterProvider(NewAnthropicProxy())
	pm.RegisterProvider(NewGeminiProxy())

	assert.NotNil(t, pm.GetProvider("openai"))
	assert.Nil(t, pm.GetProvider("nope"))
	assert.True(t, pm.IsValidProvider("openai"))
	assert.False(t, pm.IsValidProvider("missing"))
	assert.Len(t, pm.GetAllProviders(), 3)
}

func TestProviderManager_IsStreamingRequest_AnyProvider(t *testing.T) {
	pm := NewProviderManager()
	pm.RegisterProvider(NewOpenAIProxy())
	pm.RegisterProvider(NewAnthropicProxy())
	pm.RegisterProvider(NewGeminiProxy())

	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Accept", "text/event-stream")
	assert.True(t, pm.IsStreamingRequest(req))

	req2, _ := http.NewRequest("POST", "/unknown/endpoint", strings.NewReader(`{}`))
	assert.False(t, pm.IsStreamingRequest(req2))
}

func TestProviderManager_GetHealthStatus(t *testing.T) {
	pm := NewProviderManager()
	pm.RegisterProvider(NewOpenAIProxy())
	st := pm.GetHealthStatus()
	require.Contains(t, st, "openai")
}
