package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAI_ProvisionAndRevoke(t *testing.T) {
	t.Parallel()

	var deleted string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/organization/projects/proj_test/service_accounts":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": "sa_123",
				"api_key": map[string]string{
					"id":    "key_abc",
					"value": "sk-test-secret",
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/organization/projects/proj_test/service_accounts/sa_123":
			deleted = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAI("admin-key", "proj_test", srv.URL)
	res, err := p.Provision(context.Background(), "finch-hiring")
	require.NoError(t, err)
	assert.Equal(t, "sk-test-secret", res.ActualKey)
	assert.Equal(t, "sa_123", res.UpstreamID)
	assert.Equal(t, UpstreamKindOpenAIServiceAccount, res.UpstreamKind)

	err = p.Revoke(context.Background(), res.UpstreamID, res.UpstreamKind)
	require.NoError(t, err)
	assert.NotEmpty(t, deleted)
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "llm-proxy-key", SanitizeName(""))
	assert.Equal(t, "Finch-Hiring-Assistant", SanitizeName("Finch Hiring Assistant"))
}
