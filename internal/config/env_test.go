package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLoadRuntimeEnv_ReadsAllKnobs(t *testing.T) {
	t.Setenv("ENVIRONMENT", "staging")
	t.Setenv("PORT", "9100")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("LLM_PROXY_ALLOW_TEST_MODE", "1")
	t.Setenv("LLM_PROXY_ALLOW_FAKE_MODE", "true") // only the literal "1" counts
	t.Setenv("LLM_PROXY_ALLOW_DEFAULT_CONFIG", "")
	t.Setenv("COST_TRACKING_FILE", "/tmp/cost.jsonl")
	t.Setenv("LLM_PROXY_API_KEY_PREFIX", "acme")
	t.Setenv("AWS_ENDPOINT_URL", "http://localhost:8000")
	t.Setenv("SENDGRID_API_KEY", "sg")
	t.Setenv("ADMIN_PUBLIC_BASE_URL", "https://proxy.example.com")
	t.Setenv("PRESIDIO_ANALYZER_URL", "http://presidio:3000")
	t.Setenv("OCR_SIDECAR_URL", "http://ocr:8080")

	env := LoadRuntimeEnv()
	assert.Equal(t, RuntimeEnv{
		Environment:         "staging",
		Port:                "9100",
		LogLevel:            "debug",
		LogFormat:           "json",
		AllowTestMode:       true,
		AllowFakeMode:       false,
		AllowDefaultConfig:  false,
		CostTrackingFile:    "/tmp/cost.jsonl",
		APIKeyPrefix:        "acme",
		AWSEndpointURL:      "http://localhost:8000",
		SendGridAPIKey:      "sg",
		AdminPublicBaseURL:  "https://proxy.example.com",
		PresidioAnalyzerURL: "http://presidio:3000",
		OCRSidecarURL:       "http://ocr:8080",
	}, env)
}

func TestRuntimeEnv_IsExplicitLocalDev(t *testing.T) {
	cases := []struct {
		name string
		env  RuntimeEnv
		want bool
	}{
		{"empty environment", RuntimeEnv{}, true},
		{"dev", RuntimeEnv{Environment: "dev"}, true},
		{"local", RuntimeEnv{Environment: "local"}, true},
		{"production", RuntimeEnv{Environment: "production"}, false},
		{"production with override", RuntimeEnv{Environment: "production", AllowDefaultConfig: true}, true},
		{"staging", RuntimeEnv{Environment: "staging"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.env.IsExplicitLocalDev())
		})
	}
}

func TestRuntimeEnv_IsDevLike(t *testing.T) {
	assert.True(t, RuntimeEnv{Environment: "DEV"}.IsDevLike())
	assert.True(t, RuntimeEnv{Environment: "local"}.IsDevLike())
	assert.False(t, RuntimeEnv{Environment: ""}.IsDevLike(), "unset is not dev-like: raw entity logging must stay off")
	assert.False(t, RuntimeEnv{Environment: "fuzz"}.IsDevLike())
	assert.False(t, RuntimeEnv{Environment: "production"}.IsDevLike())
}

func TestLoadAdminAuthEnv(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN", "corp.example.com")
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "secret")
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECURE", "0")
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID", "cid")
	t.Setenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET", "csecret")
	t.Setenv("LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL", "https://proxy.example.com/admin/auth/callback")
	t.Setenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL", "me@example.com")

	assert.Equal(t, AdminAuthEnv{
		AllowedDomain:      "corp.example.com",
		SessionSecret:      "secret",
		SessionSecure:      "0",
		GoogleClientID:     "cid",
		GoogleClientSecret: "csecret",
		OAuthRedirectURL:   "https://proxy.example.com/admin/auth/callback",
	}, LoadAdminAuthEnv())
	assert.Equal(t, "me@example.com", AdminDevUserEmail())
}
