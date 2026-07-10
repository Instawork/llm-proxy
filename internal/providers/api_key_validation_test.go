package providers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubKeyStore struct {
	actual    string
	provider  string
	err       error
	called    int
	lastInput string
}

func (s *stubKeyStore) ValidateAndGetActualKey(_ context.Context, key string) (string, string, error) {
	s.called++
	s.lastInput = key
	return s.actual, s.provider, s.err
}

func TestOpenAI_ValidateAPIKey_Translates(t *testing.T) {
	op := NewOpenAIProxy()
	store := &stubKeyStore{actual: "real-sk-XYZ", provider: "openai"}

	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	require.NoError(t, op.ValidateAPIKey(req, store))
	assert.Equal(t, "Bearer real-sk-XYZ", req.Header.Get("Authorization"))

	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	require.NoError(t, op.ValidateAPIKey(req, store))

	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Token xyz")
	require.NoError(t, op.ValidateAPIKey(req, store))

	op2 := NewOpenAIProxy()
	errStore := &stubKeyStore{err: errors.New("nope")}
	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	assert.Error(t, op2.ValidateAPIKey(req, errStore))

	wrongStore := &stubKeyStore{actual: "real", provider: "anthropic"}
	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	assert.Error(t, op.ValidateAPIKey(req, wrongStore))
}

func TestAnthropic_ValidateAPIKey_Translates(t *testing.T) {
	ap := NewAnthropicProxy()
	store := &stubKeyStore{actual: "real-anthropic", provider: "anthropic"}

	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "iw:fake")
	require.NoError(t, ap.ValidateAPIKey(req, store))
	assert.Equal(t, "real-anthropic", req.Header.Get("x-api-key"))

	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", nil)
	require.NoError(t, ap.ValidateAPIKey(req, store))

	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "iw:fake")
	assert.Error(t, ap.ValidateAPIKey(req, &stubKeyStore{err: errors.New("nope")}))

	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "iw:fake")
	assert.Error(t, ap.ValidateAPIKey(req, &stubKeyStore{actual: "x", provider: "openai"}))
}

func TestAnthropic_ValidateAPIKey_TranslatesBearerAuthorizationHeader(t *testing.T) {
	ap := NewAnthropicProxy()
	store := &stubKeyStore{actual: "real-anthropic", provider: "anthropic"}

	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	require.NoError(t, ap.ValidateAPIKey(req, store))
	assert.Equal(t, "real-anthropic", req.Header.Get("x-api-key"))
	assert.Empty(t, req.Header.Get("Authorization"))
	assert.Equal(t, "iw:fake", store.lastInput)

	// x-api-key takes precedence over Authorization when both are present.
	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("x-api-key", "iw:direct")
	req.Header.Set("Authorization", "Bearer iw:fake")
	store2 := &stubKeyStore{actual: "real-anthropic", provider: "anthropic"}
	require.NoError(t, ap.ValidateAPIKey(req, store2))
	assert.Equal(t, "iw:direct", store2.lastInput)
	assert.Equal(t, "Bearer iw:fake", req.Header.Get("Authorization"))

	// Non-Bearer Authorization headers are left untouched and don't count as a key.
	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Basic xyz")
	require.NoError(t, ap.ValidateAPIKey(req, &stubKeyStore{}))
	assert.Equal(t, "Basic xyz", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-api-key"))
}

func TestGemini_ValidateAPIKey_Translates(t *testing.T) {
	gp := NewGeminiProxy()
	store := &stubKeyStore{actual: "real-gemini", provider: "gemini"}

	req, _ := http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent?key=iw:fake", nil)
	require.NoError(t, gp.ValidateAPIKey(req, store))
	assert.Equal(t, "real-gemini", req.URL.Query().Get("key"))

	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("x-goog-api-key", "iw:fake")
	require.NoError(t, gp.ValidateAPIKey(req, store))
	assert.Equal(t, "real-gemini", req.Header.Get("x-goog-api-key"))

	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	require.NoError(t, gp.ValidateAPIKey(req, store))

	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("x-goog-api-key", "iw:fake")
	assert.Error(t, gp.ValidateAPIKey(req, &stubKeyStore{err: errors.New("x")}))

	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("x-goog-api-key", "iw:fake")
	assert.Error(t, gp.ValidateAPIKey(req, &stubKeyStore{actual: "x", provider: "openai"}))
}

func TestGemini_ValidateAPIKey_PreservesBearerAuthorizationHeader(t *testing.T) {
	gp := NewGeminiProxy()
	store := &stubKeyStore{actual: "real-gemini", provider: "gemini"}

	req, _ := http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	require.NoError(t, gp.ValidateAPIKey(req, store))
	// Translated key stays in Authorization, unlike Anthropic which moves it.
	assert.Equal(t, "Bearer real-gemini", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-goog-api-key"))
	assert.Equal(t, "iw:fake", store.lastInput)

	// query and x-goog-api-key both take precedence over Authorization.
	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent?key=iw:query", nil)
	req.Header.Set("Authorization", "Bearer iw:fake")
	store2 := &stubKeyStore{actual: "real-gemini", provider: "gemini"}
	require.NoError(t, gp.ValidateAPIKey(req, store2))
	assert.Equal(t, "iw:query", store2.lastInput)
	assert.Equal(t, "Bearer iw:fake", req.Header.Get("Authorization"))

	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("x-goog-api-key", "iw:header")
	req.Header.Set("Authorization", "Bearer iw:fake")
	store3 := &stubKeyStore{actual: "real-gemini", provider: "gemini"}
	require.NoError(t, gp.ValidateAPIKey(req, store3))
	assert.Equal(t, "iw:header", store3.lastInput)
	assert.Equal(t, "Bearer iw:fake", req.Header.Get("Authorization"))

	// Non-Bearer Authorization headers are left untouched and don't count as a key.
	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	req.Header.Set("Authorization", "Basic xyz")
	require.NoError(t, gp.ValidateAPIKey(req, &stubKeyStore{}))
	assert.Equal(t, "Basic xyz", req.Header.Get("Authorization"))
}
