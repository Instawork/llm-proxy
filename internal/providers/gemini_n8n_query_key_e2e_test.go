// Live e2e coverage for the n8n Gemini query-key gap reported after #94.
//
// #94 exempted only GET /gemini/v1beta/models (the credential test + model
// dropdown) from the proxy-keys-in-headers-only rule. n8n's Google Gemini
// node sends its credential as ?key= on every call, including
// :generateContent, so chat model runs still 401. This test drives the exact
// request n8n's node makes — a created iw: key on ?key= only, no auth header
// — through the real APIKeyValidationMiddleware + Gemini reverse proxy to a
// live upstream, mirroring TestGeminiIntegration_CostTracking_CreatedKey in
// cost_tracking_integration_test.go.
//
//	GEMINI_API_KEY=... go test ./internal/providers -run TestGeminiIntegration_N8NQueryKeyOnGenerateContent -v
package providers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

func TestGeminiIntegration_N8NQueryKeyOnGenerateContent(t *testing.T) {
	upstreamKey := os.Getenv("GEMINI_API_KEY")
	if upstreamKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName:       "n8n-query-key-e2e-keys",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewGeminiProxy())

	r := mux.NewRouter()
	r.Use(middleware.APIKeyValidationMiddleware(pm, store, false, nil))
	r.PathPrefix("/gemini/").Handler(pm.GetProvider("gemini").Proxy())

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	created, err := store.CreateKey(t.Context(), "gemini", upstreamKey, "n8n query-key e2e test", 0, nil, nil)
	require.NoError(t, err)

	// n8n's googlePalmApi credential is query-string auth (qs: { key: ... }),
	// so the chat model node sends the proxy key ONLY as ?key= here — no
	// Authorization or x-goog-api-key header, exactly like the model list GET
	// that #94 already allows, but on a metered POST endpoint instead.
	body, err := json.Marshal(map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": "Reply with the single word OK."}}},
		},
		"generationConfig": map[string]interface{}{"maxOutputTokens": 16},
	})
	require.NoError(t, err)

	url := server.URL + "/gemini/v1beta/models/gemini-2.5-flash:generateContent?key=" + created.PK
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode,
		"n8n-style ?key=-only generateContent call was rejected: %s", string(respBody))
}
