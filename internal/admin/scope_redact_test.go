package admin

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedactScopeKey(t *testing.T) {
	longKey := apikeys.KeyPrefix + "65e9abba6d75bc3b20cb392be618742d6467a67c3f54c0a47577a488b65b0dd9"

	tests := []struct {
		in   string
		want string
	}{
		{"global", "global"},
		{"provider:gemini", "provider:gemini"},
		{"model:gpt-4o", "model:gpt-4o"},
		{"key:" + longKey, "key:••••0dd9"},
		{"user:ip:203.0.113.7:36371", "user:•••.•••.•••.•:36371"},
		{"user:alice", "user:alice"},
		{"user:Canvas Agent", "user:Canvas Agent"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, RedactScopeKey(tc.in))
		})
	}
}

func TestSanitizeLimitsSnapshot_RedactsScopes(t *testing.T) {
	snap := ratelimit.LimitsSnapshot{
		Minute: &ratelimit.WindowSnapshot{
			Counters: map[string]ratelimit.CounterSnapshot{
				"key:iw:abcdefghijklmnop": {Requests: 2, Tokens: 10},
				"global":                  {Requests: 1},
			},
		},
	}
	sanitizeLimitsSnapshot(&snap)
	require.Contains(t, snap.Minute.Counters, "key:••••mnop")
	require.Contains(t, snap.Minute.Counters, "global")
}

func TestSanitizeRateLimitOverrides_RedactsKeys(t *testing.T) {
	out := sanitizeRateLimitOverrides(config.RateLimitOverrides{
		PerKey: map[string]config.LimitsConfig{
			"iw:long-secret-value": {RequestsPerMinute: 5},
		},
		PerUser: map[string]config.LimitsConfig{
			"alice@example.com": {TokensPerDay: 1000},
		},
	})
	require.Contains(t, out.PerKey, "key:••••alue")
	require.Contains(t, out.PerUser, "user:alice@example.com")
}

func TestMergeRedactedCounters_CollapsesSameSuffix(t *testing.T) {
	k1 := apikeys.KeyPrefix + "aaaa1111111111111111111111111111111111111111111111111111abcd"
	k2 := apikeys.KeyPrefix + "bbbb2222222222222222222222222222222222222222222222222222abcd"

	out := mergeRedactedCounters(map[string]ratelimit.CounterSnapshot{
		"key:" + k1: {Requests: 3, Tokens: 30},
		"key:" + k2: {Requests: 2, Tokens: 20},
	})

	assert.Len(t, out, 1)
	assert.Equal(t, ratelimit.CounterSnapshot{Requests: 5, Tokens: 50}, out["key:••••abcd"])
}
