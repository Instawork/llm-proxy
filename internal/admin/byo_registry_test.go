package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleListBYOKeys_AggregatesSources(t *testing.T) {
	h, store := testAdminHandler(t)
	ctx := context.Background()

	anthropicHash := apikeys.CredentialHashSuffix("sk-ant-live")
	anthropicMasked := "sk-ant-…" + anthropicHash
	geminiMasked := "AIza…" + apikeys.CredentialHashSuffix("AIza-live")

	_, err := store.BanBYOCredential(ctx, "gemini", geminiMasked, "admin@example.com", "policy")
	require.NoError(t, err)

	h.deps.PIISummary = func() map[string]interface{} {
		return map[string]interface{}{
			"available": true,
			"top_keys": []nameCountRow{
				{Name: anthropicMasked, Count: 12},
				{Name: "sk-iw-abc12345678901234567890123456789012", Count: 99},
			},
			"recent": []piiRecentRow{
				{Provider: "anthropic", KeyID: anthropicMasked},
			},
		}
	}
	h.deps.CostSummary = func() map[string]interface{} {
		return map[string]interface{}{
			"available": true,
			"by_key": []costKeyRow{
				{KeyID: anthropicMasked, Requests: 3, SpendUSD: 1.25},
			},
		}
	}

	req := authenticatedRequestAs(t, h, "admin@example.com", http.MethodGet, "/admin/api/byo-keys", nil)
	rec := httptest.NewRecorder()
	h.handleListBYOKeys(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var rows []BYOKeyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rows))
	require.Len(t, rows, 2)

	var anthropic, gemini *BYOKeyResponse
	for i := range rows {
		switch rows[i].Provider {
		case "anthropic":
			anthropic = &rows[i]
		case "gemini":
			gemini = &rows[i]
		}
	}
	require.NotNil(t, anthropic)
	require.NotNil(t, gemini)

	assert.Equal(t, anthropicMasked, anthropic.MaskedID)
	assert.Equal(t, int64(13), anthropic.PIIScans)
	assert.Equal(t, int64(3), anthropic.CostRequests)
	assert.Equal(t, 1.25, anthropic.SpendUSD)
	assert.ElementsMatch(t, []string{"cost", "pii"}, anthropic.Sources)
	assert.False(t, anthropic.Banned)

	assert.True(t, gemini.Banned)
	assert.Equal(t, geminiMasked, gemini.MaskedID)
	assert.Equal(t, []string{"ban"}, gemini.Sources)
}
