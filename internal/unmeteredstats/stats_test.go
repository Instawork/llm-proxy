package unmeteredstats

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
)

func TestEndpointLabel(t *testing.T) {
	cases := map[string]struct {
		path   string
		status int
		want   string
	}{
		"strips provider prefix":        {"/openai/v1/embeddings", 200, "/v1/embeddings"},
		"strips meta and provider":      {"/meta/user-42/anthropic/v1/models", 200, "/v1/models"},
		"gemini model with action":      {"/gemini/v1beta/models/gemini-2.0-flash:embedContent", 200, "/v1beta/models/{model}:embedContent"},
		"bedrock model id":              {"/bedrock/model/anthropic.claude-3-haiku-20240307-v1:0/invoke", 200, "/model/{model}/invoke"},
		"resource ids collapse":         {"/openai/v1/files/file-abc123XYZ/content", 200, "/v1/files/{id}/content"},
		"long opaque ids collapse":      {"/openai/v1/batches/a1b2c3d4e5f6a7b8c9d0e1f2", 200, "/v1/batches/{id}"},
		"failed call carries status":    {"/openai/v1/chat/completions", 401, "/v1/chat/completions (HTTP 401)"},
		"unknown prefix left untouched": {"/v1/models/gemini-pro:countTokens", 200, "/v1/models/{model}:countTokens"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, EndpointLabel(tc.path, tc.status))
		})
	}
}

func TestRecorderSnapshotByKey(t *testing.T) {
	r := NewRecorder()
	r.RecordRequest("iw:a", "/v1/embeddings")
	r.RecordRequest("iw:a", "/v1/embeddings")
	r.RecordRequest("iw:a", "/v1/models")
	r.RecordRequest("", "/v1/models")

	snap := r.Snapshot()
	assert.Equal(t, int64(4), snap["requests_today"])
	assert.Equal(t, "memory", snap["backend"])
	byKey := snap["by_key"].(map[string]map[string]int64)
	assert.Equal(t, map[string]int64{"/v1/embeddings": 2, "/v1/models": 1}, byKey["iw:a"])
	assert.NotContains(t, byKey, "")
	assert.Equal(t, map[string]int64{"/v1/embeddings": 2, "/v1/models": 2}, adminrollup.NameCountMapFromSnap(snap["by_endpoint"]))
}

func TestRecorderCapsEndpointsPerKey(t *testing.T) {
	r := NewRecorder()
	for i := 0; i < maxEndpointsPerKey+5; i++ {
		r.RecordRequest("iw:a", fmt.Sprintf("/v1/junk-%d (HTTP 404)", i))
	}
	byKey := r.Snapshot()["by_key"].(map[string]map[string]int64)["iw:a"]
	assert.Len(t, byKey, maxEndpointsPerKey+1)
	assert.Equal(t, int64(5), byKey[otherEndpoint])
}

func TestRecorderMergesFleetRollup(t *testing.T) {
	store, err := adminrollup.NewStore(adminrollup.Config{Enabled: true, Backend: "memory"})
	require.NoError(t, err)

	other := NewRecorder()
	other.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricUnmetered))
	other.RecordRequest("iw:a", "/v1/embeddings")
	other.RecordRequest("iw:b", "/v1/models")
	other.FlushRollup()

	local := NewRecorder()
	local.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricUnmetered))
	local.RecordRequest("iw:a", "/v1/audio/transcriptions")
	local.FlushRollup()

	snap := local.Snapshot()
	assert.Equal(t, "redis", snap["backend"])
	assert.Equal(t, int64(3), snap["requests_today"])
	byKey := snap["by_key"].(map[string]map[string]int64)
	assert.Equal(t, map[string]int64{"/v1/embeddings": 1, "/v1/audio/transcriptions": 1}, byKey["iw:a"])
	assert.Equal(t, map[string]int64{"/v1/models": 1}, byKey["iw:b"])
}
