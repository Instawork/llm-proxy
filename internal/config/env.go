package config

import (
	"os"
	"strings"
)

// RuntimeEnv is the single place that enumerates the process-level
// environment variables the proxy honours. Callers load it once (main's
// runServer, or a gating helper that must observe the live environment) and
// pass values down instead of sprinkling os.Getenv through the wiring code,
// so the env contract is visible in one struct and trivially testable.
//
// YAML remains the source of truth for feature configuration; these are the
// deployment knobs (ports, secrets, endpoint overrides, safety gates) that
// legitimately differ per process.
type RuntimeEnv struct {
	// Environment is ENVIRONMENT (dev, staging, production, fuzz, …).
	Environment string
	// Port is PORT; empty means the caller's default.
	Port string

	// LogLevel / LogFormat are LOG_LEVEL and LOG_FORMAT.
	LogLevel  string
	LogFormat string

	// AllowTestMode is LLM_PROXY_ALLOW_TEST_MODE=1: lets the circuit-breaker
	// test-mode header be honoured (integration tests only).
	AllowTestMode bool
	// AllowFakeMode is LLM_PROXY_ALLOW_FAKE_MODE=1: lets the fake upstream
	// transport be installed (never in production).
	AllowFakeMode bool
	// AllowDefaultConfig is LLM_PROXY_ALLOW_DEFAULT_CONFIG=1: downgrades
	// startup failures that would otherwise be fatal to warnings.
	AllowDefaultConfig bool

	// CostTrackingFile is COST_TRACKING_FILE, the file-backend output path.
	CostTrackingFile string
	// APIKeyPrefix is LLM_PROXY_API_KEY_PREFIX, the base of minted key prefixes.
	APIKeyPrefix string
	// AWSEndpointURL is AWS_ENDPOINT_URL (dynamodb-local and friends).
	AWSEndpointURL string
	// SendGridAPIKey is SENDGRID_API_KEY for admin notifications.
	SendGridAPIKey string
	// AdminPublicBaseURL is ADMIN_PUBLIC_BASE_URL used in notification links.
	AdminPublicBaseURL string

	// PresidioAnalyzerURL / OCRSidecarURL override the YAML sidecar URLs.
	PresidioAnalyzerURL string
	OCRSidecarURL       string
}

// LoadRuntimeEnv reads RuntimeEnv from the process environment.
func LoadRuntimeEnv() RuntimeEnv {
	return RuntimeEnv{
		Environment:         os.Getenv("ENVIRONMENT"),
		Port:                os.Getenv("PORT"),
		LogLevel:            os.Getenv("LOG_LEVEL"),
		LogFormat:           os.Getenv("LOG_FORMAT"),
		AllowTestMode:       envFlag("LLM_PROXY_ALLOW_TEST_MODE"),
		AllowFakeMode:       envFlag("LLM_PROXY_ALLOW_FAKE_MODE"),
		AllowDefaultConfig:  envFlag("LLM_PROXY_ALLOW_DEFAULT_CONFIG"),
		CostTrackingFile:    os.Getenv("COST_TRACKING_FILE"),
		APIKeyPrefix:        os.Getenv("LLM_PROXY_API_KEY_PREFIX"),
		AWSEndpointURL:      os.Getenv("AWS_ENDPOINT_URL"),
		SendGridAPIKey:      os.Getenv("SENDGRID_API_KEY"),
		AdminPublicBaseURL:  os.Getenv("ADMIN_PUBLIC_BASE_URL"),
		PresidioAnalyzerURL: os.Getenv("PRESIDIO_ANALYZER_URL"),
		OCRSidecarURL:       os.Getenv("OCR_SIDECAR_URL"),
	}
}

// IsExplicitLocalDev reports whether the process is opted in to degraded
// local-dev startup: LLM_PROXY_ALLOW_DEFAULT_CONFIG=1 or a dev/local/empty
// ENVIRONMENT. Startup failures that would be fatal elsewhere are downgraded
// to warnings here.
func (e RuntimeEnv) IsExplicitLocalDev() bool {
	return e.AllowDefaultConfig || e.Environment == "" || e.Environment == "dev" || e.Environment == "local"
}

// IsDevLike reports whether ENVIRONMENT is dev or local (case-insensitive),
// the only environments where raw PII entity logging may be enabled.
func (e RuntimeEnv) IsDevLike() bool {
	env := strings.ToLower(e.Environment)
	return env == "dev" || env == "local"
}

// AdminAuthEnv holds the admin dashboard's authentication secrets and
// overrides. Kept separate from RuntimeEnv so the admin package can load
// exactly what it needs without depending on proxy-wide knobs.
type AdminAuthEnv struct {
	// AllowedDomain is LLM_PROXY_ADMIN_ALLOWED_DOMAIN; overrides YAML.
	AllowedDomain string
	// SessionSecret is LLM_PROXY_ADMIN_SESSION_SECRET (required outside dev bypass).
	SessionSecret string
	// SessionSecure is LLM_PROXY_ADMIN_SESSION_SECURE: "1", "0" or "" (default).
	SessionSecure string
	// GoogleClientID / GoogleClientSecret are the OAuth client credentials.
	GoogleClientID     string
	GoogleClientSecret string
	// OAuthRedirectURL is LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL; empty derives
	// the callback from the request.
	OAuthRedirectURL string
}

// LoadAdminAuthEnv reads AdminAuthEnv from the process environment.
func LoadAdminAuthEnv() AdminAuthEnv {
	return AdminAuthEnv{
		AllowedDomain:      os.Getenv("LLM_PROXY_ADMIN_ALLOWED_DOMAIN"),
		SessionSecret:      os.Getenv("LLM_PROXY_ADMIN_SESSION_SECRET"),
		SessionSecure:      os.Getenv("LLM_PROXY_ADMIN_SESSION_SECURE"),
		GoogleClientID:     os.Getenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("LLM_PROXY_ADMIN_GOOGLE_CLIENT_SECRET"),
		OAuthRedirectURL:   os.Getenv("LLM_PROXY_ADMIN_OAUTH_REDIRECT_URL"),
	}
}

// AdminDevUserEmail returns LLM_PROXY_ADMIN_DEV_USER_EMAIL, the identity
// minted by the dev-bypass login. Read per request (not at boot) so local
// developers can switch identities without restarting the proxy.
func AdminDevUserEmail() string {
	return os.Getenv("LLM_PROXY_ADMIN_DEV_USER_EMAIL")
}

func envFlag(name string) bool {
	return os.Getenv(name) == "1"
}
