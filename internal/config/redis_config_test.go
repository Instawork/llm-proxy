package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"}, {12345, "12,345"},
		{1234567, "1,234,567"}, {1000000000, "1,000,000,000"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, formatNumber(c.in))
	}
}

func TestRedisConfig_MarshalJSON_Redacts(t *testing.T) {
	cases := []struct {
		name        string
		cfg         RedisConfig
		wantURL     string
		wantPwField bool
	}{
		{
			name:        "no userinfo, no password",
			cfg:         RedisConfig{URL: "redis://localhost:6379/0"},
			wantURL:     "redis://localhost:6379/0",
			wantPwField: false,
		},
		{
			name:        "with userinfo in URL",
			cfg:         RedisConfig{URL: "redis://user:pw@host:6379/3"},
			wantURL:     "redis://%2A%2A%2A:%2A%2A%2A@host:6379/3",
			wantPwField: false,
		},
		{
			name:        "explicit password set",
			cfg:         RedisConfig{Address: "host:6379", Password: "supersecret"},
			wantURL:     "",
			wantPwField: true,
		},
		{
			name:        "unparseable URL is wholly redacted",
			cfg:         RedisConfig{URL: "::not a url::"},
			wantURL:     "***",
			wantPwField: false,
		},
		{
			name:        "empty url stays empty",
			cfg:         RedisConfig{},
			wantURL:     "",
			wantPwField: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := json.Marshal(c.cfg)
			require.NoError(t, err)
			var out map[string]interface{}
			require.NoError(t, json.Unmarshal(b, &out))
			if c.wantURL != "" {
				assert.Equal(t, c.wantURL, out["url"])
			} else {
				_, has := out["url"]
				assert.False(t, has, "url should be omitted when empty")
			}
			pw, has := out["password"]
			if c.wantPwField {
				assert.True(t, has)
				assert.Equal(t, "***REDACTED***", pw)
			} else {
				assert.False(t, has, "password should be omitted when empty")
			}
		})
	}
}

func TestRedactedRedisURL_Direct(t *testing.T) {
	assert.Equal(t, "", redactedRedisURL(""))
	assert.Equal(t, "redis://localhost:6379", redactedRedisURL("redis://localhost:6379"))
	assert.Equal(t, "redis://%2A%2A%2A:%2A%2A%2A@h:6379", redactedRedisURL("redis://u:p@h:6379"))
}
