package admin

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/stretchr/testify/assert"
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
		{"user:ip:192.168.65.1:36371", "user:•••.•••.•••.•:36371"},
		{"user:alice", "user:••••lice"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, RedactScopeKey(tc.in))
		})
	}
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
