package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// formatNumber formats a number with commas for better readability
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	// Convert to string and add commas
	str := fmt.Sprintf("%d", n)
	result := ""

	for i, char := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(char)
	}

	return result
}

// YAMLConfig represents the main YAML configuration structure
type YAMLConfig struct {
	// Global settings
	Enabled bool `yaml:"enabled"`

	// Features configuration
	Features FeaturesConfig `yaml:"features"`

	// Providers configuration
	Providers map[string]ProviderConfig `yaml:"providers"`

	// RetiredModels maps provider -> model slug -> retirement metadata for
	// request-time short-circuiting before upstream calls.
	RetiredModels map[string]map[string]RetiredModelEntry `yaml:"retired_models"`
}

// RetiredModelEntry describes a single retired model. The map key in
// retired_models is the canonical slug; Aliases lists alternate slugs that
// resolve to the same retirement metadata.
type RetiredModelEntry struct {
	RetiredDate string   `yaml:"retired_date"`
	Replacement string   `yaml:"replacement"`
	Aliases     []string `yaml:"aliases,omitempty"`
}

// FeaturesConfig represents feature toggle configuration
type FeaturesConfig struct {
	CostTracking     CostTrackingConfig     `yaml:"cost_tracking"`
	APIKeyManagement APIKeyManagementConfig `yaml:"api_key_management"`
	RateLimiting     RateLimitingConfig     `yaml:"rate_limiting"`
	CircuitBreaker   CircuitBreakerConfig   `yaml:"circuit_breaker"`
	PIIRedact        PIIRedactConfig        `yaml:"pii_redact"`
	BYOKeys          BYOKeysConfig          `yaml:"byo_keys"`
	IDGate           IDGateConfig           `yaml:"id_gate"`
	RedactAPI        RedactAPIConfig        `yaml:"redact_api"`
	FakeUpstream     FakeUpstreamConfig     `yaml:"fake_upstream"`
	AdminDashboard   AdminDashboardConfig   `yaml:"admin_dashboard"`
	History          HistoryConfig          `yaml:"history"`
	ClientGzip       ClientGzipConfig       `yaml:"client_gzip"`
}

// ClientGzipConfig optionally gzip-compresses non-streaming responses to
// clients that requested Accept-Encoding: gzip. Off by default.
type ClientGzipConfig struct {
	Enabled bool `yaml:"enabled"`
}

// BYOKeysConfig gates whether callers may authenticate with raw provider
// credentials (sk-ant-/AIza/...) instead of proxy iw-* keys. Disabled on
// the standalone production service; co-located app sidecars keep it on.
type BYOKeysConfig struct {
	Enabled bool `yaml:"enabled"`
}

// PIIRedactConfig configures the proxy-side PII redactor that calls the
// Presidio analyzer sidecar before bodies are written to logs / cost
// transports / Datadog. The redactor never modifies the request that
// goes to the upstream LLM — only the copy persisted for observability.
//
// Default behaviour is "disabled". Until you have a reachable sidecar in
// the same task definition (see live/production/services/llm-proxy in
// the infrastructure repo), leave this off.
type PIIRedactConfig struct {
	// Enabled gates the feature. When false, the middleware is not
	// installed and no /analyze calls happen.
	Enabled bool `yaml:"enabled"`

	// AnalyzerURL is the base URL of the Presidio analyzer sidecar.
	// Required when Enabled is true. Production: localhost in the same
	// ECS task. docker-compose: http://presidio:3000.
	AnalyzerURL string `yaml:"analyzer_url"`

	// FailMode controls behaviour when the sidecar errors or times out.
	//
	//   "open"   (default) — log a warning and pass the request through
	//                        un-redacted. The upstream LLM still serves
	//                        the user; only the persisted copy may be
	//                        un-redacted for that single request.
	//   "closed"           — abort the request with 503. Pick this only
	//                        when the regulatory cost of an un-redacted
	//                        log line outweighs availability.
	FailMode string `yaml:"fail_mode"`

	// TimeoutMs caps each /analyze round trip. Default: 3000ms (see base.yml).
	// Tune downward only after baselining warm sidecar p99 if latency matters.
	TimeoutMs int `yaml:"timeout_ms"`

	// TimeoutMsPer100KB adds extra /analyze budget per 100 KiB of request
	// body (see redact.analyze100KiB). Zero disables scaling. Large tool
	// results (Redshift table_acl, etc.) need the extra headroom.
	TimeoutMsPer100KB int `yaml:"timeout_ms_per_100kb,omitempty"`

	// TimeoutMsMax caps the scaled deadline. Zero defaults to 30s when
	// timeout_ms_per_100kb is set.
	TimeoutMsMax int `yaml:"timeout_ms_max,omitempty"`

	// AnalyzeConcurrency caps parallel Presidio /analyze calls per JSON body.
	// Zero defaults to 4. Tune down if the sidecar shows queue latency.
	AnalyzeConcurrency int `yaml:"analyze_concurrency,omitempty"`

	// ScoreThreshold is the minimum Presidio confidence score for a
	// span to be redacted. Default: 0.5.
	ScoreThreshold float64 `yaml:"score_threshold"`

	// EntityTypes scopes which recognizers run. Empty falls back to the
	// audited default list in redact.DefaultEntityTypes. Pass a subset
	// to shave latency on known-safe payload shapes — never widen it
	// here, since redact.New filters out anything that isn't already on
	// the in-code allowlist (a code review is the only way to extend it).
	EntityTypes []string `yaml:"entity_types,omitempty"`

	// Language forwards to the /analyze ``language`` parameter.
	// Default: "en".
	Language string `yaml:"language"`

	// MaxBodyBytes caps the request body size we'll send to /analyze.
	// Bodies above this threshold short-circuit with a WARN log
	// (tagged path / provider / body_bytes / max_body_bytes) and
	// flow upstream un-redacted (no redacted copy in context, so any
	// log/cost transport that fingerprints request bodies falls back
	// to metadata only for that request).
	//
	// Default: 0 → middleware applies its own 1048576-byte (1 MiB)
	// default. That fits virtually all chat / completions / embeddings
	// shapes and most vision payloads — the design bias is "redact
	// almost everything and only let truly unusual uploads slip
	// through". Lower this if sidecar latency becomes a problem;
	// raise it if the WARN counter trends up in Datadog.
	MaxBodyBytes int `yaml:"max_body_bytes,omitempty"`

	// AllowPerKeyOverride installs the redactor middleware when true even if
	// Enabled is false, so individual iw: keys can opt in via redact_pii.
	AllowPerKeyOverride bool `yaml:"allow_per_key_override"`

	// WirePlaceholders sends scrubbed placeholder text to the upstream LLM
	// instead of observability-only redaction. Default: true when enabled.
	WirePlaceholders *bool `yaml:"wire_placeholders,omitempty"`

	// DefaultAllowStreaming controls whether wire-mode requests may keep
	// stream:true. Per-key allow_streaming overrides this. Default: true.
	DefaultAllowStreaming *bool `yaml:"default_allow_streaming,omitempty"`

	// Datadog configures the dogstatsd sink for PII redaction metrics
	// (llm.pii.*). Independent of cost_tracking and circuit_breaker.
	Datadog *DatadogTransportConfig `yaml:"datadog,omitempty"`

	// AllowTestEmails, when nil or true, lets obvious fixture emails pass
	// through middle-ground filtering (example.com, test@*, dev@*).
	AllowTestEmails *bool `yaml:"allow_test_emails,omitempty"`

	// AnalyzeCache caches Presidio span lists per content block (memory
	// and/or Redis) so unchanged chat history skips redundant /analyze calls.
	AnalyzeCache PIIRedactAnalyzeCacheConfig `yaml:"analyze_cache,omitempty"`
}

// PIIRedactAnalyzeCacheConfig configures optional Presidio span caching.
type PIIRedactAnalyzeCacheConfig struct {
	Enabled    bool                              `yaml:"enabled"`
	TTLSeconds int                               `yaml:"ttl_seconds,omitempty"`
	Memory     PIIRedactAnalyzeCacheMemoryConfig `yaml:"memory"`
	Redis      PIIRedactAnalyzeCacheRedisConfig  `yaml:"redis"`
}

type PIIRedactAnalyzeCacheMemoryConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxEntries int  `yaml:"max_entries,omitempty"`
}

type PIIRedactAnalyzeCacheRedisConfig struct {
	Enabled bool         `yaml:"enabled"`
	Redis   *RedisConfig `yaml:"redis,omitempty"`
}

// IDGateConfig configures OCR + Presidio scanning of embedded chat images to
// block government identity documents before they reach an upstream LLM.
type IDGateConfig struct {
	// Enabled installs the ID gate middleware when true.
	Enabled bool `yaml:"enabled"`

	// OCRSidecarURL is the base URL of the OnnxTR OCR sidecar.
	// docker-compose: http://ocr-sidecar:8000. ECS: localhost in the same task.
	OCRSidecarURL string `yaml:"ocr_sidecar_url"`

	// FailMode controls behaviour when OCR or Presidio errors.
	// "open" (default) passes through; "closed" aborts with 503.
	FailMode string `yaml:"fail_mode"`

	// ScoreThreshold is the minimum Presidio confidence to block. Default: 0.4.
	ScoreThreshold float64 `yaml:"score_threshold"`

	// EntityTypes scopes gov-ID detection. Empty -> redact.DefaultGovIDEntityTypes.
	EntityTypes []string `yaml:"entity_types,omitempty"`

	// MaxBodyBytes caps request bodies scanned for images. 0 -> middleware 1 MiB default.
	MaxBodyBytes int `yaml:"max_body_bytes,omitempty"`

	// MaxImageBytes caps each decoded embedded image. Default: 10 MiB.
	MaxImageBytes int `yaml:"max_image_bytes,omitempty"`

	// TimeoutMs caps each OCR round trip. Default: 30000.
	TimeoutMs int `yaml:"timeout_ms"`

	// ImageConcurrency caps how many embedded images in a single request are
	// OCR'd + analyzed in parallel. 0 -> middleware default of 4. Raise to
	// reduce multi-image gate latency when the OCR fleet has spare capacity.
	ImageConcurrency int `yaml:"image_concurrency,omitempty"`

	// Datadog configures the dogstatsd sink for ID gate metrics (llm.id_gate.*).
	Datadog *DatadogTransportConfig `yaml:"datadog,omitempty"`
}

// RedactAPIConfig gates the standalone POST /redact endpoint for generic
// text redaction (e.g. Cursor hooks). Uses the same Presidio sidecar as
// pii_redact; analyzer_url is read from features.pii_redact.analyzer_url.
type RedactAPIConfig struct {
	// Enabled registers POST /redact when true and a redactor can be built.
	Enabled bool `yaml:"enabled"`

	// FailMode must be "closed" (or empty, which defaults to closed at
	// startup). Open/fail-through is not supported — hooks must not receive
	// unredacted text when Presidio is unavailable.
	FailMode string `yaml:"fail_mode"`

	// RequestsPerMinute caps POST /redact per iw-* API key. 0 means unlimited.
	RequestsPerMinute int `yaml:"requests_per_minute,omitempty"`

	// MaxBodyBytes caps request body size. 0 inherits 1048576 (1 MiB).
	MaxBodyBytes int `yaml:"max_body_bytes,omitempty"`

	// DevAllowUnauthenticated skips iw-* auth on POST /redact. Allowed only when
	// ENVIRONMENT=dev and features.admin_dashboard.dev_bypass_login is true.
	DevAllowUnauthenticated bool `yaml:"dev_allow_unauthenticated,omitempty"`
}

// FakeUpstreamConfig gates synthetic LLM responses for local fuzzing.
// Requires LLM_PROXY_ALLOW_FAKE_MODE=1 in addition to enabled: true.
type FakeUpstreamConfig struct {
	Enabled          bool    `yaml:"enabled"`
	ChaosFailureRate float64 `yaml:"chaos_failure_rate"`
	ChaosSeed        int64   `yaml:"chaos_seed"`
	LatencyMS        int     `yaml:"latency_ms"`
	JitterMS         int     `yaml:"jitter_ms"`
}

// AdminDashboardConfig gates the /admin UI and JSON API.
type AdminDashboardConfig struct {
	Enabled bool `yaml:"enabled"`
	// AllowedDomain restricts Google OAuth sign-in to a single hosted domain
	// (e.g. example.com). Defaults to example.com when unset.
	AllowedDomain string `yaml:"allowed_domain"`
	// DevCORSOrigin allows the Vite dev server to call the admin API.
	DevCORSOrigin string `yaml:"dev_cors_origin"`
	// DevBypassLogin enables POST /admin/auth/dev-login for local development
	// without Google OAuth. Must stay false outside dev configs.
	DevBypassLogin bool `yaml:"dev_bypass_login"`
	// PublicBaseURL is the externally-visible origin of the proxy
	// (e.g. https://llm.example.com). Used for SDK/proxy base URLs in share
	// responses so links don't leak internal hostnames (llm-proxy:9002).
	// Share *page* URLs use DevCORSOrigin when dev_bypass_login is true.
	// Falls back to the ADMIN_PUBLIC_BASE_URL env var, then to
	// http://localhost:{PORT} in dev (when DevCORSOrigin or DevBypassLogin
	// is set), then to the inbound request's scheme+host. No trailing slash.
	PublicBaseURL string `yaml:"public_base_url"`
	// Rollups persists daily admin metrics to Redis for historical charts.
	Rollups AdminRollupsConfig `yaml:"rollups"`
	// PIIOffBypassAdmins lists admin emails allowed to create/update keys with
	// PII redaction disabled on a non-Bedrock provider. When empty, a built-in
	// default list applies (see apikeys.SetPIIOffNonBedrockBypassAdmins).
	// Prefer setting this in YAML so roster changes don't require a deploy.
	PIIOffBypassAdmins []string `yaml:"pii_off_bypass_admins"`
	// Users configures the DynamoDB-backed admin user roster and RBAC.
	Users AdminUsersConfig `yaml:"users"`
	// EditorLimits caps what editors may set when creating/updating keys.
	EditorLimits EditorLimitsConfig `yaml:"editor_limits"`
	// ViewerLimits caps viewer personal-key permissions.
	ViewerLimits ViewerLimitsConfig `yaml:"viewer_limits"`
}

// AdminUsersConfig configures the admin user DynamoDB store.
type AdminUsersConfig struct {
	DynamoDB AdminUsersDynamoDBConfig `yaml:"dynamodb"`
}

// AdminUsersDynamoDBConfig is DynamoDB settings for admin users.
type AdminUsersDynamoDBConfig struct {
	Region          string `yaml:"region"`
	TableName       string `yaml:"table_name"`
	EndpointURL     string `yaml:"endpoint_url"`
	AutoCreateTable bool   `yaml:"auto_create_table"`
}

// EditorLimitsConfig caps editor key-management permissions.
type EditorLimitsConfig struct {
	// MaxDailyCostLimitCents is the maximum daily_cost_limit (cents) editors
	// may set on keys. Zero means no cap.
	MaxDailyCostLimitCents int64 `yaml:"max_daily_cost_limit_cents"`
}

// ViewerLimitsConfig caps viewer personal-key permissions.
type ViewerLimitsConfig struct {
	// PersonalMonthlyCostLimitCents is the monthly spend cap (cents) applied
	// to viewer personal keys. Defaults to 2000 when unset.
	PersonalMonthlyCostLimitCents int64 `yaml:"personal_monthly_cost_limit_cents"`
}

// AdminRollupsConfig configures daily rollups for the admin UI.
type AdminRollupsConfig struct {
	Enabled bool `yaml:"enabled"`
	// Backend selects the rollup store: "redis" (durable, survives restart) or
	// "memory" (in-process, for local dev / tests with no Redis dependency).
	// When empty it is inferred: "redis" if a Redis address/URL is configured,
	// otherwise "memory".
	Backend string `yaml:"backend"`
	// Redis uses a dedicated DB (default 6) on the shared cluster.
	Redis *RedisConfig `yaml:"redis,omitempty"`
	// RetentionDays TTL on archived daily keys (default 90).
	RetentionDays int `yaml:"retention_days"`
	// HistoryDays number of prior days returned in daily_history (default 30).
	HistoryDays int `yaml:"history_days"`
}

// HistoryConfig configures durable raw-event row history (local JSONL or S3).
// Independent of admin_dashboard.rollups (Redis aggregates).
type HistoryConfig struct {
	// Backend selects row storage: "none" (default), "local", or "s3".
	Backend string `yaml:"backend"`
	// Role distinguishes deployment shape in filenames: "sidecar" or "global".
	Role string `yaml:"role"`
	// InstanceID overrides auto-detected instance identity (else HISTORY_INSTANCE_ID / HOSTNAME).
	InstanceID string `yaml:"instance_id"`
	// Streams limits which event families are archived; empty means all.
	Streams []string `yaml:"streams,omitempty"`
	// MaxRecords flush threshold (default 1000).
	MaxRecords int `yaml:"max_records"`
	// MaxBytes flush threshold (default 8 MiB).
	MaxBytes int `yaml:"max_bytes"`
	// MaxAgeSeconds time-based flush interval (default 300).
	MaxAgeSeconds int `yaml:"max_age_seconds"`
	// Gzip compresses each chunk (default true when omitted).
	Gzip *bool `yaml:"gzip,omitempty"`

	Local *HistoryLocalConfig `yaml:"local,omitempty"`
	S3    *HistoryS3Config    `yaml:"s3,omitempty"`
}

// HistoryLocalConfig is used when history.backend is "local".
type HistoryLocalConfig struct {
	Dir string `yaml:"dir"`
}

// HistoryS3Config is used when history.backend is "s3".
type HistoryS3Config struct {
	Bucket      string `yaml:"bucket"`
	Prefix      string `yaml:"prefix"`
	Region      string `yaml:"region"`
	EndpointURL string `yaml:"endpoint_url"`
}

// CircuitBreakerConfig configures the proxy-side circuit breaker and retry
// policies for provider degradation detection.
type CircuitBreakerConfig struct {
	// Enabled gates the feature entirely.
	Enabled bool `yaml:"enabled"`

	// Mode selects the operational mode.  "log" (default) is observe-only:
	// the transport does one round trip, classifies the response, records
	// failures in the store for observability, emits counterfactual log
	// lines, and returns the real upstream response — no retries, no
	// fast-fail on open circuit, no synthetic 503s.  "enforce" runs the
	// full retry + fast-fail + synthetic-response behaviour.
	//
	// The safe default is "log" so that partially rolled-out or
	// misconfigured deployments never alter user-facing traffic.
	Mode string `yaml:"mode"`

	// Backend selects the state store: "memory" (default, single-process) or
	// "redis" (recommended for production with multiple proxy instances).
	Backend string `yaml:"backend"`

	// FailureThreshold is the number of terminal failures within WindowSeconds
	// that trips the circuit.  Default: 5.
	FailureThreshold int `yaml:"failure_threshold"`

	// WindowSeconds is the sliding-window TTL for failure counters.  Default: 120.
	WindowSeconds int `yaml:"window_seconds"`

	// CooldownSeconds is how long the circuit stays Open before probing.
	// Default: 300.
	CooldownSeconds int `yaml:"cooldown_seconds"`

	// MaxTransientRetries is the retry limit for degraded-class failures.
	// Default: 2.
	MaxTransientRetries int `yaml:"max_transient_retries"`

	// MaxRateLimitRetries is the retry limit for rate-limit failures.
	// Default: 2.
	MaxRateLimitRetries int `yaml:"max_rate_limit_retries"`

	// RetryContributionMode controls whether retried failures count toward the
	// circuit-breaker threshold.  Values: "off", "log" (default), "on".
	RetryContributionMode string `yaml:"retry_contribution_mode"`

	// GlobalRateLimitEscalationWindow is the number of seconds of sustained
	// global rate-limit failures that must elapse before escalating to
	// provider_degraded.  Default: 60.
	GlobalRateLimitEscalationWindow int `yaml:"global_rate_limit_escalation_window"`

	// Redis connection settings when Backend is "redis".
	Redis *RedisConfig `yaml:"redis,omitempty"`

	// TestModeEnabled allows the X-LLM-Proxy-Test-Mode header to be honoured.
	// Should be false in production.
	TestModeEnabled bool `yaml:"test_mode_enabled"`

	// DegradedSignal overrides the substring embedded in synthesised
	// degraded error bodies so downstream clients can detect proxy-
	// originated degradation.  Leave empty to use the default
	// (see circuit.DefaultDegradedSignal).  Change it only if your clients
	// already key off a different, project-specific tag.
	DegradedSignal string `yaml:"degraded_signal,omitempty"`

	// BypassAllowed gates whether the transport will honour the
	// X-LLM-Proxy-Bypass-Circuit header / llm_proxy_bypass_circuit
	// query parameter.  Modelled as *bool so an unset field defaults
	// to TRUE (callers without a fallback should be able to opt out
	// of fast-fail by default), and explicit `bypass_allowed: false`
	// disables the safety valve entirely without ambiguity.  See the
	// BypassHeader docstring in internal/circuit/types.go for the
	// full rationale.
	BypassAllowed *bool `yaml:"bypass_allowed,omitempty"`

	// PerProviderRollupThreshold is the number of distinct per-key
	// (typically per-model) breakers that must open within
	// PerProviderRollupWindowSeconds before the entire provider is
	// treated as Open and ALL keys for that provider are fast-failed.
	// Zero (the default) disables the rollup feature, so behaviour
	// matches v1 per-key keying.  Recommended production starting
	// point: 3 with a 300-second window.
	PerProviderRollupThreshold int `yaml:"per_provider_rollup_threshold,omitempty"`

	// PerProviderRollupWindowSeconds is the sliding-window TTL for
	// per-key open events used by the rollup signal.  Default: 300.
	// Has no effect when PerProviderRollupThreshold == 0.
	PerProviderRollupWindowSeconds int `yaml:"per_provider_rollup_window_seconds,omitempty"`

	// BypassReasonAllowlist optionally restricts which inbound
	// X-LLM-Proxy-Bypass-Reason values are emitted verbatim on
	// `circuit.bypass` dogstatsd tags + the per-bypass log line.  Any
	// reason NOT in this set is reported as "other".  Set to a small
	// canonical vocabulary in production to keep tag cardinality
	// bounded; empty (the default) accepts any well-formed reason.
	// See circuit.Config.BypassReasonAllowlist for the full rationale.
	BypassReasonAllowlist []string `yaml:"bypass_reason_allowlist,omitempty"`

	// Datadog configures the dogstatsd sink for circuit-breaker metrics
	// (llm.circuit.*).  Independent of cost_tracking transports — disabling
	// cost tracking or its Datadog transport must not affect circuit
	// observability, and vice versa.
	Datadog *DatadogTransportConfig `yaml:"datadog,omitempty"`
}

// CostTrackingConfig represents cost tracking feature configuration
type CostTrackingConfig struct {
	Enabled       bool              `yaml:"enabled"`
	Async         bool              `yaml:"async,omitempty"`          // Enable async tracking with workers (default: false - sync)
	Workers       int               `yaml:"workers,omitempty"`        // Number of worker goroutines for async tracking (default: 5)
	QueueSize     int               `yaml:"queue_size,omitempty"`     // Size of the async tracking queue (default: 1000)
	FlushInterval int               `yaml:"flush_interval,omitempty"` // Interval in seconds to flush pending records (default: 15)
	Transports    []TransportConfig `yaml:"transports,omitempty"`     // Multiple transport configs
	// FailClosedOnReadError makes daily cost-limit enforcement reject
	// cost-limited requests (503) when the fleet-wide spend read is degraded
	// (Redis rollup bound but unreachable), instead of silently falling back
	// to per-instance enforcement. Default false (fail-open for availability).
	FailClosedOnReadError bool `yaml:"fail_closed_on_read_error,omitempty"`
}

// TransportConfig represents cost tracking transport configuration
type TransportConfig struct {
	Type     string                   `yaml:"type"` // "file", "dynamodb", or "datadog"
	File     *FileTransportConfig     `yaml:"file,omitempty"`
	DynamoDB *DynamoDBTransportConfig `yaml:"dynamodb,omitempty"`
	Datadog  *DatadogTransportConfig  `yaml:"datadog,omitempty"`
}

// FileTransportConfig represents file-based transport configuration
type FileTransportConfig struct {
	Path string `yaml:"path"`
}

// DynamoDBTransportConfig represents DynamoDB transport configuration
type DynamoDBTransportConfig struct {
	TableName string `yaml:"table_name"`
	Region    string `yaml:"region"`
	// AutoCreateTable mirrors APIKeyManagementConfig.AutoCreateTable —
	// local dev only. Production must pre-provision the table.
	AutoCreateTable bool `yaml:"auto_create_table"`
}

// DatadogTransportConfig represents Datadog transport configuration
type DatadogTransportConfig struct {
	Host       string   `yaml:"host"`        // DogStatsD host (default: localhost)
	Port       string   `yaml:"port"`        // DogStatsD port (default: 8125)
	Namespace  string   `yaml:"namespace"`   // Namespace to prefix metrics (default: "llm_proxy")
	Tags       []string `yaml:"tags"`        // Global tags to apply to all metrics
	SampleRate float64  `yaml:"sample_rate"` // Global sample rate (default: 1.0)
}

// APIKeyManagementConfig represents API key management configuration
type APIKeyManagementConfig struct {
	Enabled   bool   `yaml:"enabled"`
	TableName string `yaml:"table_name"`
	Region    string `yaml:"region"`
	// EndpointURL overrides the DynamoDB API endpoint (local dev / docker-compose).
	// Falls back to AWS_ENDPOINT_URL when empty.
	EndpointURL string `yaml:"endpoint_url"`
	// AutoCreateTable, when true, allows the API key store to call
	// CreateTable when the configured DynamoDB table is missing. This is
	// only safe in local development; production/staging deployments must
	// pre-provision the table via Terraform and leave this false.
	AutoCreateTable bool `yaml:"auto_create_table"`
	// KeyPrefix is the prefix base (without separator, e.g. "iw") used for
	// proxy API keys. New keys are generated as "sk-<base>-<random>", and
	// "sk-<base>-", "<base>-", "<base>_", and "<base>:" are accepted on
	// lookup. Blank falls back to apikeys.DefaultKeyPrefixBase. Overridable
	// at runtime via the LLM_PROXY_API_KEY_PREFIX env var.
	KeyPrefix string `yaml:"key_prefix"`
	// Provisioning configures automatic upstream key minting for the admin UI.
	Provisioning KeyProvisioningConfig `yaml:"provisioning,omitempty"`
}

// KeyProvisioningConfig controls server-side upstream API key creation.
type KeyProvisioningConfig struct {
	Enabled bool `yaml:"enabled"`
	// DevFake mints local-only upstream credentials without calling vendor APIs.
	DevFake   bool                        `yaml:"dev_fake,omitempty"`
	OpenAI    OpenAIProvisioningConfig    `yaml:"openai,omitempty"`
	Gemini    GeminiProvisioningConfig    `yaml:"gemini,omitempty"`
	Anthropic AnthropicProvisioningConfig `yaml:"anthropic,omitempty"`
}

// OpenAIProvisioningConfig mints keys via the OpenAI Admin API.
type OpenAIProvisioningConfig struct {
	Enabled   bool   `yaml:"enabled"`
	ProjectID string `yaml:"project_id"`
}

// GeminiProvisioningConfig mints keys via the GCP API Keys API.
type GeminiProvisioningConfig struct {
	Enabled      bool   `yaml:"enabled"`
	GCPProjectID string `yaml:"gcp_project_id"`
}

// AnthropicProvisioningConfig assigns shared Anthropic keys by trust tier.
type AnthropicProvisioningConfig struct {
	Enabled     bool              `yaml:"enabled"`
	DefaultTier string            `yaml:"default_tier,omitempty"`
	Tiers       map[string]string `yaml:"tiers,omitempty"`
	// PoolRedisKey enables the legacy Redis pool provisioner when tiers are unset.
	PoolRedisKey string `yaml:"pool_redis_key,omitempty"`
}

// RateLimitingConfig represents rate limiting feature configuration
type RateLimitingConfig struct {
	Enabled    bool               `yaml:"enabled"`
	Backend    string             `yaml:"backend"` // "memory" or "redis"
	Limits     LimitsConfig       `yaml:"limits"`
	Overrides  RateLimitOverrides `yaml:"overrides,omitempty"`
	Estimation EstimationConfig   `yaml:"estimation,omitempty"`
	Redis      *RedisConfig       `yaml:"redis,omitempty"`
}

// LimitsConfig contains the per-window limits. Zero or negative means unlimited.
type LimitsConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute"`
	RequestsPerDay    int `yaml:"requests_per_day"`
	TokensPerDay      int `yaml:"tokens_per_day"`
}

// RateLimitOverrides allow per-entity limit overrides
type RateLimitOverrides struct {
	PerKey   map[string]LimitsConfig `yaml:"per_key,omitempty"`
	PerUser  map[string]LimitsConfig `yaml:"per_user,omitempty"`
	PerModel map[string]LimitsConfig `yaml:"per_model,omitempty"`
}

// EstimationConfig controls request token estimation behavior
type EstimationConfig struct {
	MaxSampleBytes        int            `yaml:"max_sample_bytes"`
	BytesPerToken         int            `yaml:"bytes_per_token"`
	CharsPerToken         int            `yaml:"chars_per_token"`
	ProviderCharsPerToken map[string]int `yaml:"provider_chars_per_token,omitempty"`
}

// RedisConfig contains Redis backend settings
type RedisConfig struct {
	// URL is a full Redis connection string (e.g. `redis://:pw@host:6379/3`
	// or `rediss://...` for TLS).  Takes priority over Address/Password
	// when set.  YAML unmarshalling does not expand `${VAR}` / `$VAR`
	// tokens; callers may expand them later during wiring/initialization
	// so secrets (e.g. a REDIS_URL rendered from a secret manager) can
	// be passed in via container environment without baking credentials
	// into YAML.
	URL      string `yaml:"url"`
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
	// DB pins which Redis database the circuit breaker uses.  When the
	// URL already encodes a DB (via `/N`) this field overrides it — set
	// this explicitly when sharing a Redis instance with another tenant
	// that owns a different DB.  DBSet tracks whether YAML explicitly
	// provided db, allowing an explicit db: 0 to override a URL DB.
	DB    int  `yaml:"db"`
	DBSet bool `yaml:"-"`
}

type redisConfigYAML struct {
	URL      string `yaml:"url,omitempty"`
	Address  string `yaml:"address,omitempty"`
	Password string `yaml:"password,omitempty"`
	DB       *int   `yaml:"db,omitempty"`
}

// UnmarshalYAML records whether db was explicitly present so db: 0 can be
// distinguished from an omitted DB value.
func (c *RedisConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw redisConfigYAML
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.URL = raw.URL
	c.Address = raw.Address
	c.Password = raw.Password
	if raw.DB != nil {
		c.DB = *raw.DB
		c.DBSet = true
	} else {
		c.DB = 0
		c.DBSet = false
	}
	return nil
}

// MarshalYAML preserves the distinction between omitted db and explicit db: 0
// across config merge round-trips.
func (c RedisConfig) MarshalYAML() (interface{}, error) {
	out := redisConfigYAML{
		URL:      c.URL,
		Address:  c.Address,
		Password: c.Password,
	}
	if c.DBSet {
		db := c.DB
		out.DB = &db
	}
	return out, nil
}

// MarshalJSON redacts secret-bearing fields on RedisConfig so the full
// configuration can be safely dumped by the --version / /health paths
// (or any other JSON diagnostic output) without leaking credentials.
// Password is replaced with a fixed sentinel when present, and any
// userinfo in URL is stripped by re-marshalling the parsed URL.  The
// output schema is stable: every field the YAML form uses is still
// present so diagnostic tooling does not have to special-case
// redacted payloads.
//
// Note: this only affects JSON marshalling.  The YAML-tagged struct
// can still be serialised unchanged for round-trip writes (see
// SaveConfigurationToFile) — in that code path the config is written
// back to an operator-owned file where the original credentials must
// be preserved.
func (c RedisConfig) MarshalJSON() ([]byte, error) {
	const redacted = "***REDACTED***"
	// Use an alias type to avoid an infinite MarshalJSON recursion on
	// the embedded struct literal.  The alias has no methods.
	type redisConfigJSON struct {
		URL      string `json:"url,omitempty"`
		Address  string `json:"address,omitempty"`
		Password string `json:"password,omitempty"`
		DB       int    `json:"db"`
	}
	out := redisConfigJSON{
		URL:     redactedRedisURL(c.URL),
		Address: c.Address,
		DB:      c.DB,
	}
	if c.Password != "" {
		out.Password = redacted
	}
	return json.Marshal(out)
}

// redactedRedisURL returns u with any embedded userinfo (`user:pw@`)
// replaced by `***:***@`.  An unparseable URL is fully redacted to
// "***" since we cannot safely strip credentials we cannot locate.
func redactedRedisURL(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return "***"
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("***", "***")
	}
	return parsed.String()
}

// ProviderConfig represents configuration for a specific provider
type ProviderConfig struct {
	Enabled bool                   `yaml:"enabled"`
	Models  map[string]ModelConfig `yaml:"models"`
}

// ModelConfig represents configuration for a specific model
type ModelConfig struct {
	Enabled     bool     `yaml:"enabled"`
	Deprecated  bool     `yaml:"deprecated,omitempty"`
	Replacement string   `yaml:"replacement,omitempty"`
	Aliases     []string `yaml:"aliases,omitempty"` // Alternative model names
	// Pricing can be a single price, or a list of tiers.
	Pricing interface{} `yaml:"pricing,omitempty"`
	// ProjectID scopes upstream requests for this model to a specific provider
	// project. Currently consumed only by the Bedrock Mantle proxy, which sends
	// it as the OpenAI-Project header so Mantle resolves data-retention (and
	// other project-scoped policy) against that project instead of the account
	// default. Supports ${VAR} env expansion so account-specific ids stay out of
	// the committed YAML. Empty leaves the account-level policy in force.
	ProjectID string `yaml:"project_id,omitempty"`
}

// Pricing represents a simple input/output cost structure.
type Pricing struct {
	Input  float64 `yaml:"input"`  // Cost per 1M input tokens in USD
	Output float64 `yaml:"output"` // Cost per 1M output tokens in USD
}

// PricingTier represents a pricing tier with a token threshold.
type PricingTier struct {
	Threshold int     `yaml:"threshold"` // The token threshold for this tier
	Input     float64 `yaml:"input"`     // Cost per 1M input tokens in USD
	Output    float64 `yaml:"output"`    // Cost per 1M output tokens in USD
}

// ModelPricing represents pricing information for a model, with optional overrides for aliases.
type ModelPricing struct {
	Default   Pricing            `yaml:"default,omitempty"`
	Tiers     []PricingTier      `yaml:"tiers,omitempty"`
	Overrides map[string]Pricing `yaml:"overrides,omitempty"` // Pricing for specific model aliases
}

// LoadYAMLConfig loads configuration from a YAML file
func LoadYAMLConfig(filename string) (*YAMLConfig, error) {
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return GetDefaultYAMLConfig(), nil
	}

	// Read the file
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filename, err)
	}

	// Parse YAML
	var config YAMLConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	// Validate and fill in calculated fields
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Parse the pricing structures.
	if err := config.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing structures: %w", err)
	}

	return &config, nil
}

// loadYAMLConfigWithoutValidation loads configuration from a YAML file without validation
// This is used for environment-specific configs that may only contain partial overrides
func loadYAMLConfigWithoutValidation(filename string) (*YAMLConfig, error) {
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return &YAMLConfig{}, nil // Return empty config if file doesn't exist
	}

	// Read the file
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filename, err)
	}

	// Parse YAML
	var config YAMLConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	// Note: We intentionally skip validation and pricing parsing here
	// since this is just for environment overrides
	return &config, nil
}

// LoadEnvironmentConfig loads base configuration and overlays environment-specific
// configuration based on the ENVIRONMENT variable (defaults to "dev"). When
// LLM_PROXY_CONFIG_PROFILE is set, its YAML file is merged last — e.g.
// ENVIRONMENT=production + LLM_PROXY_CONFIG_PROFILE=sidecar keeps prod infra
// settings while applying the sidecar feature toggles.
func LoadEnvironmentConfig() (*YAMLConfig, error) {
	configDir := "configs"

	// Load base configuration
	baseConfig, err := LoadYAMLConfig(filepath.Join(configDir, "base.yml"))
	if err != nil {
		return nil, fmt.Errorf("failed to load base configuration: %w", err)
	}

	// Get environment from environment variable, default to "dev"
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}
	slog.Info("Loading environment configuration", "environment", env)

	mergedConfig := baseConfig

	// Load environment-specific configuration (skip validation since it's just overrides)
	envConfigPath := filepath.Join(configDir, fmt.Sprintf("%s.yml", env))
	envConfig, err := loadYAMLConfigWithoutValidation(envConfigPath)
	if err != nil {
		// If environment config doesn't exist, just use base config
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load environment configuration for %s: %w", env, err)
		}
	} else {
		mergedConfig, err = mergeConfigs(mergedConfig, envConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to merge configurations: %w", err)
		}
	}

	profile := os.Getenv("LLM_PROXY_CONFIG_PROFILE")
	if profile != "" {
		slog.Info("Loading config profile overlay", "profile", profile)
		profileConfigPath := filepath.Join(configDir, fmt.Sprintf("%s.yml", profile))
		if _, err := os.Stat(profileConfigPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("config profile %q not found at %s", profile, profileConfigPath)
		} else if err != nil {
			return nil, fmt.Errorf("failed to stat config profile %s: %w", profile, err)
		}
		mergedConfig, err = mergeConfigFromYAMLFile(mergedConfig, profileConfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to merge config profile %s: %w", profile, err)
		}
	}

	return mergedConfig, nil
}

// mergeConfigs merges the environment config into the base config
// Environment config values override base config values
func mergeConfigs(base, env *YAMLConfig) (*YAMLConfig, error) {
	// Convert both configs to YAML bytes
	baseBytes, err := yaml.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal base config: %w", err)
	}

	envBytes, err := yaml.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal environment config: %w", err)
	}

	// Parse both as generic maps for merging
	var baseMap map[string]interface{}
	if err := yaml.Unmarshal(baseBytes, &baseMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal base config to map: %w", err)
	}

	var envMap map[string]interface{}
	if err := yaml.Unmarshal(envBytes, &envMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal environment config to map: %w", err)
	}

	// Deep merge environment config into base config
	mergedMap := deepMerge(baseMap, envMap)

	// Convert merged map back to YAML bytes
	mergedBytes, err := yaml.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	// Parse back into YAMLConfig struct
	var mergedConfig YAMLConfig
	if err := yaml.Unmarshal(mergedBytes, &mergedConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged config: %w", err)
	}

	// Validate and parse pricing
	if err := mergedConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid merged configuration: %w", err)
	}

	if err := mergedConfig.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing in merged configuration: %w", err)
	}

	return &mergedConfig, nil
}

// mergeConfigFromYAMLFile deep-merges a YAML file into base. Only keys present
// in the file participate — partial overlays (e.g. configs/sidecar.yml) do not
// inject zero-value feature toggles from a struct round-trip.
func mergeConfigFromYAMLFile(base *YAMLConfig, filename string) (*YAMLConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filename, err)
	}

	var overlayMap map[string]interface{}
	if err := yaml.Unmarshal(data, &overlayMap); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config %s: %w", filename, err)
	}

	baseBytes, err := yaml.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal base config: %w", err)
	}

	var baseMap map[string]interface{}
	if err := yaml.Unmarshal(baseBytes, &baseMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal base config to map: %w", err)
	}

	mergedMap := deepMerge(baseMap, overlayMap)

	mergedBytes, err := yaml.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	var mergedConfig YAMLConfig
	if err := yaml.Unmarshal(mergedBytes, &mergedConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged config: %w", err)
	}

	if err := mergedConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid merged configuration: %w", err)
	}

	if err := mergedConfig.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing in merged configuration: %w", err)
	}

	return &mergedConfig, nil
}

// deepMerge recursively merges map b into map a
// Values in b override values in a
func deepMerge(a, b map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy all values from a
	for k, v := range a {
		result[k] = v
	}

	// Merge values from b
	for k, v := range b {
		if existingValue, exists := result[k]; exists {
			// If both values are maps, merge them recursively
			if existingMap, ok := existingValue.(map[string]interface{}); ok {
				if newMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMerge(existingMap, newMap)
					continue
				}
			}
		}
		// Otherwise, override with the new value
		result[k] = v
	}

	return result
}

// LoadAndMergeConfigs loads multiple configuration files and merges them in order
func LoadAndMergeConfigs(filePaths []string) (*YAMLConfig, error) {
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("no configuration files provided")
	}

	// Load the first config as the base
	baseConfig, err := LoadYAMLConfig(filePaths[0])
	if err != nil {
		return nil, fmt.Errorf("failed to load base config from %s: %w", filePaths[0], err)
	}

	// If only one file, return it
	if len(filePaths) == 1 {
		return baseConfig, nil
	}

	// Merge additional configs in order
	mergedConfig := baseConfig
	for i := 1; i < len(filePaths); i++ {
		// Load the overlay config without validation (like environment configs)
		overlayConfig, err := loadYAMLConfigWithoutValidation(filePaths[i])
		if err != nil {
			return nil, fmt.Errorf("failed to load overlay config from %s: %w", filePaths[i], err)
		}

		// Merge the overlay into the current merged config
		mergedConfig, err = mergeConfigs(mergedConfig, overlayConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to merge config from %s: %w", filePaths[i], err)
		}
	}

	return mergedConfig, nil
}

// SaveYAMLConfig saves configuration to a YAML file
func (c *YAMLConfig) SaveYAMLConfig(filename string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0o644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Validate validates the configuration
func (c *YAMLConfig) Validate() error {
	if c.Providers == nil {
		return fmt.Errorf("providers configuration is required")
	}

	// Validate transport configuration if cost tracking is enabled
	if c.Features.CostTracking.Enabled {
		if err := c.validateTransportConfig(); err != nil {
			return fmt.Errorf("invalid transport configuration: %w", err)
		}
	}

	// Validate rate limiting configuration if enabled
	if c.Features.RateLimiting.Enabled {
		if err := c.validateRateLimitingConfig(); err != nil {
			return fmt.Errorf("invalid rate limiting configuration: %w", err)
		}
	}

	// Validate circuit breaker configuration if enabled
	if c.Features.CircuitBreaker.Enabled {
		if err := c.validateCircuitBreakerConfig(); err != nil {
			return fmt.Errorf("invalid CircuitBreakerConfig: %w", err)
		}
	}

	// Validate API key management configuration if enabled. Previously this
	// block was missing, so a config that enabled api_key_management with
	// empty table_name/region silently fell through and only failed inside
	// apikeys.NewStore() at startup with a less specific error.
	if c.Features.APIKeyManagement.Enabled {
		if err := c.validateAPIKeyManagementConfig(); err != nil {
			return fmt.Errorf("invalid api_key_management configuration: %w", err)
		}
	}

	// Validate PII redaction config if enabled so a typo in fail_mode or
	// a missing analyzer_url is surfaced via --validate-config rather than
	// at first request.
	if c.Features.PIIRedact.Enabled || c.Features.PIIRedact.AllowPerKeyOverride || c.Features.RedactAPI.Enabled || c.Features.IDGate.Enabled {
		if err := c.validatePIIRedactConfig(); err != nil {
			return fmt.Errorf("invalid pii_redact configuration: %w", err)
		}
	}

	if c.Features.IDGate.Enabled {
		if err := c.validateIDGateConfig(); err != nil {
			return fmt.Errorf("invalid id_gate configuration: %w", err)
		}
	}

	if c.Features.RedactAPI.Enabled {
		if err := c.validateRedactAPIConfig(); err != nil {
			return fmt.Errorf("invalid redact_api configuration: %w", err)
		}
	}

	if c.Features.AdminDashboard.EditorLimits.MaxDailyCostLimitCents < 0 {
		return fmt.Errorf("invalid admin_dashboard configuration: editor_limits.max_daily_cost_limit_cents cannot be negative")
	}

	if c.Features.AdminDashboard.ViewerLimits.PersonalMonthlyCostLimitCents < 0 {
		return fmt.Errorf("invalid admin_dashboard configuration: viewer_limits.personal_monthly_cost_limit_cents cannot be negative")
	}

	if err := c.validateRetiredModels(); err != nil {
		return fmt.Errorf("invalid retired_models configuration: %w", err)
	}

	return nil
}

func (c *YAMLConfig) validateRetiredModels() error {
	if c == nil || len(c.RetiredModels) == 0 {
		return nil
	}
	for provider, models := range c.RetiredModels {
		if len(models) == 0 {
			continue
		}
		seen := make(map[string]string)
		for canonical, entry := range models {
			if canonical == "" {
				return fmt.Errorf("provider %q has an empty retired model key", provider)
			}
			if entry.RetiredDate == "" {
				return fmt.Errorf("provider %q retired model %q: retired_date is required", provider, canonical)
			}
			if entry.Replacement == "" {
				return fmt.Errorf("provider %q retired model %q: replacement is required", provider, canonical)
			}
			if other, ok := seen[canonical]; ok {
				return fmt.Errorf("provider %q: duplicate retired slug %q (also listed under %q)", provider, canonical, other)
			}
			seen[canonical] = canonical
			for _, alias := range entry.Aliases {
				if alias == "" {
					return fmt.Errorf("provider %q retired model %q: empty alias", provider, canonical)
				}
				if alias == canonical {
					return fmt.Errorf("provider %q retired model %q: alias duplicates canonical name", provider, canonical)
				}
				if other, ok := seen[alias]; ok {
					return fmt.Errorf("provider %q: retired slug %q is listed twice (under %q and %q)", provider, alias, other, canonical)
				}
				seen[alias] = canonical
			}
		}
	}
	return nil
}

// validatePIIRedactConfig enforces shape constraints on the pii_redact
// feature block. Only invoked when Enabled is true.
func (c *YAMLConfig) validatePIIRedactConfig() error {
	r := c.Features.PIIRedact

	if r.AnalyzerURL == "" {
		return fmt.Errorf("analyzer_url is required when pii_redact, allow_per_key_override, or redact_api is enabled")
	}

	switch r.FailMode {
	case "", "open", "closed":
		// "" defaults to "open" at construction time.
	default:
		return fmt.Errorf("fail_mode must be one of open, closed (got %q)", r.FailMode)
	}

	if r.TimeoutMs < 0 {
		return fmt.Errorf("timeout_ms cannot be negative")
	}
	if r.TimeoutMsPer100KB < 0 {
		return fmt.Errorf("timeout_ms_per_100kb cannot be negative")
	}
	if r.TimeoutMsMax < 0 {
		return fmt.Errorf("timeout_ms_max cannot be negative")
	}
	if r.AnalyzeConcurrency < 0 {
		return fmt.Errorf("analyze_concurrency cannot be negative")
	}
	if r.ScoreThreshold < 0 || r.ScoreThreshold > 1 {
		return fmt.Errorf("score_threshold must be in [0, 1] (got %v)", r.ScoreThreshold)
	}
	if r.MaxBodyBytes < 0 {
		return fmt.Errorf("max_body_bytes cannot be negative")
	}
	if r.AnalyzeCache.TTLSeconds < 0 {
		return fmt.Errorf("analyze_cache.ttl_seconds cannot be negative")
	}
	if r.AnalyzeCache.Memory.MaxEntries < 0 {
		return fmt.Errorf("analyze_cache.memory.max_entries cannot be negative")
	}
	if r.AnalyzeCache.Enabled && r.AnalyzeCache.Redis.Enabled && r.AnalyzeCache.Redis.Redis == nil {
		return fmt.Errorf("analyze_cache.redis.redis is required when analyze_cache.redis.enabled is true")
	}
	env := os.Getenv("ENVIRONMENT")
	if env == "production" && r.WirePlaceholders != nil && !*r.WirePlaceholders {
		return fmt.Errorf("wire_placeholders: false is not allowed when ENVIRONMENT=production")
	}
	return nil
}

func (c *YAMLConfig) validateIDGateConfig() error {
	g := c.Features.IDGate

	if g.OCRSidecarURL == "" {
		return fmt.Errorf("ocr_sidecar_url is required when id_gate is enabled")
	}
	if c.Features.PIIRedact.AnalyzerURL == "" {
		return fmt.Errorf("features.pii_redact.analyzer_url is required when id_gate is enabled")
	}

	switch g.FailMode {
	case "", "open", "closed":
	default:
		return fmt.Errorf("fail_mode must be one of open, closed (got %q)", g.FailMode)
	}
	if g.ScoreThreshold < 0 || g.ScoreThreshold > 1 {
		return fmt.Errorf("score_threshold must be in [0, 1] (got %v)", g.ScoreThreshold)
	}
	if g.MaxBodyBytes < 0 {
		return fmt.Errorf("max_body_bytes cannot be negative")
	}
	if g.MaxImageBytes < 0 {
		return fmt.Errorf("max_image_bytes cannot be negative")
	}
	if g.TimeoutMs < 0 {
		return fmt.Errorf("timeout_ms cannot be negative")
	}
	return nil
}

func (c *YAMLConfig) validateRedactAPIConfig() error {
	r := c.Features.RedactAPI

	switch r.FailMode {
	case "", "closed":
	default:
		return fmt.Errorf("fail_mode must be closed or empty (got %q); open is not supported for redact_api", r.FailMode)
	}
	if r.RequestsPerMinute < 0 {
		return fmt.Errorf("requests_per_minute cannot be negative")
	}
	if r.MaxBodyBytes < 0 {
		return fmt.Errorf("max_body_bytes cannot be negative")
	}
	if r.DevAllowUnauthenticated {
		env := os.Getenv("ENVIRONMENT")
		if env == "" {
			env = "dev"
		}
		if env != "dev" {
			return fmt.Errorf("dev_allow_unauthenticated is only allowed when ENVIRONMENT=dev (got %q)", env)
		}
		if !c.Features.AdminDashboard.DevBypassLogin {
			return fmt.Errorf("dev_allow_unauthenticated requires features.admin_dashboard.dev_bypass_login")
		}
	}
	if !c.Features.APIKeyManagement.Enabled && !r.DevAllowUnauthenticated {
		return fmt.Errorf("api_key_management must be enabled when redact_api is enabled")
	}
	return nil
}

// validateAPIKeyManagementConfig validates the api_key_management feature
// configuration. It enforces that table_name and region are explicitly set
// so a misconfigured deployment cannot fall through to AWS default
// resolution (and accidentally point at a different account or region).
func (c *YAMLConfig) validateAPIKeyManagementConfig() error {
	akm := c.Features.APIKeyManagement
	if akm.TableName == "" {
		return fmt.Errorf("table_name is required when api_key_management is enabled")
	}
	if akm.Region == "" {
		return fmt.Errorf("region is required when api_key_management is enabled")
	}
	return nil
}

// validateTransportConfig validates the transport configuration
func (c *YAMLConfig) validateTransportConfig() error {
	transports := c.GetAllTransports()
	if len(transports) == 0 {
		return fmt.Errorf("at least one transport configuration is required")
	}

	for i, transport := range transports {
		if err := c.validateSingleTransport(transport); err != nil {
			return fmt.Errorf("transport %d: %w", i, err)
		}
	}

	return nil
}

// validateSingleTransport validates a single transport configuration
func (c *YAMLConfig) validateSingleTransport(transport TransportConfig) error {
	switch transport.Type {
	case "file":
		if transport.File == nil {
			return fmt.Errorf("file transport configuration is required when type is 'file'")
		}
		if transport.File.Path == "" {
			return fmt.Errorf("file path is required for file transport")
		}
	case "dynamodb":
		if transport.DynamoDB == nil {
			return fmt.Errorf("dynamodb transport configuration is required when type is 'dynamodb'")
		}
		if transport.DynamoDB.TableName == "" {
			return fmt.Errorf("table_name is required for dynamodb transport")
		}
		if transport.DynamoDB.Region == "" {
			return fmt.Errorf("region is required for dynamodb transport")
		}
	case "datadog":
		if transport.Datadog == nil {
			return fmt.Errorf("datadog transport configuration is required when type is 'datadog'")
		}
		// Host and Port have defaults, so no validation needed
	case "":
		return fmt.Errorf("transport type is required")
	default:
		return fmt.Errorf("unsupported transport type: %s (supported: file, dynamodb, datadog)", transport.Type)
	}

	return nil
}

// GetAllTransports returns all configured transports
func (c *YAMLConfig) GetAllTransports() []TransportConfig {
	if !c.Features.CostTracking.Enabled {
		return nil
	}

	return c.Features.CostTracking.Transports
}

// validateRateLimitingConfig validates the rate limiting configuration
func (c *YAMLConfig) validateRateLimitingConfig() error {
	rl := c.Features.RateLimiting
	switch rl.Backend {
	case "", "memory":
		// default to memory
	case "redis":
		if rl.Redis == nil || (rl.Redis.URL == "" && rl.Redis.Address == "") {
			return fmt.Errorf("redis backend selected but redis.url and redis.address are empty")
		}
	default:
		return fmt.Errorf("unsupported backend: %s (supported: memory, redis)", rl.Backend)
	}

	if rl.Estimation.BytesPerToken < 0 {
		return fmt.Errorf("estimation.bytes_per_token cannot be negative")
	}
	if rl.Estimation.CharsPerToken < 0 {
		return fmt.Errorf("estimation.chars_per_token cannot be negative")
	}
	if rl.Estimation.MaxSampleBytes < -1 {
		return fmt.Errorf("estimation.max_sample_bytes cannot be less than -1")
	}
	// Validate per-provider char overrides if present
	if rl.Estimation.ProviderCharsPerToken != nil {
		for prov, v := range rl.Estimation.ProviderCharsPerToken {
			if v < 0 {
				return fmt.Errorf("estimation.provider_chars_per_token[%s] cannot be negative", prov)
			}
		}
	}
	return nil
}

// validateCircuitBreakerConfig validates the circuit_breaker feature config
// early so --validate-config catches rollout typos before runtime.
func (c *YAMLConfig) validateCircuitBreakerConfig() error {
	cb := c.Features.CircuitBreaker

	switch cb.Mode {
	case "", "log", "enforce":
	default:
		return fmt.Errorf("mode must be one of log, enforce (got %q)", cb.Mode)
	}

	switch cb.Backend {
	case "", "memory":
		// default to memory
	case "redis":
		if cb.Redis == nil {
			return fmt.Errorf("backend redis requires RedisConfig")
		}
		if cb.Redis.URL == "" && cb.Redis.Address == "" {
			return fmt.Errorf("backend redis requires RedisConfig.url or RedisConfig.address")
		}
	default:
		return fmt.Errorf("backend must be one of memory, redis (got %q)", cb.Backend)
	}

	if cb.FailureThreshold < 0 {
		return fmt.Errorf("failure_threshold cannot be negative")
	}
	if cb.FailureThreshold == 0 {
		return fmt.Errorf("failure_threshold must be greater than 0")
	}
	if cb.WindowSeconds < 0 {
		return fmt.Errorf("window_seconds cannot be negative")
	}
	if cb.WindowSeconds == 0 {
		return fmt.Errorf("window_seconds must be greater than 0")
	}
	if cb.CooldownSeconds < 0 {
		return fmt.Errorf("cooldown_seconds cannot be negative")
	}
	if cb.CooldownSeconds == 0 {
		return fmt.Errorf("cooldown_seconds must be greater than 0")
	}
	if cb.MaxTransientRetries < 0 {
		return fmt.Errorf("max_transient_retries cannot be negative")
	}
	if cb.MaxRateLimitRetries < 0 {
		return fmt.Errorf("max_rate_limit_retries cannot be negative")
	}
	if cb.GlobalRateLimitEscalationWindow < 0 {
		return fmt.Errorf("global_rate_limit_escalation_window cannot be negative")
	}

	switch cb.RetryContributionMode {
	case "", "off", "log", "on":
	default:
		return fmt.Errorf("retry_contribution_mode must be one of off, log, on (got %q)", cb.RetryContributionMode)
	}

	if cb.PerProviderRollupThreshold < 0 {
		return fmt.Errorf("per_provider_rollup_threshold cannot be negative (got %d)", cb.PerProviderRollupThreshold)
	}
	if cb.PerProviderRollupWindowSeconds < 0 {
		return fmt.Errorf("per_provider_rollup_window_seconds cannot be negative (got %d)", cb.PerProviderRollupWindowSeconds)
	}

	return nil
}

// ParsePricing iterates through all models and parses the flexible `Pricing` field
// into a structured `ModelPricing` object.
func (c *YAMLConfig) ParsePricing() error {
	for providerName, provider := range c.Providers {
		for modelName, model := range provider.Models {
			if model.Pricing == nil {
				continue
			}

			parsedPricing, err := parseModelPricing(model.Pricing)
			if err != nil {
				return fmt.Errorf("error parsing pricing for %s/%s: %w", providerName, modelName, err)
			}
			model.Pricing = parsedPricing
			provider.Models[modelName] = model
		}
	}
	return nil
}

// parseModelPricing handles the logic of parsing the `interface{}` pricing field.
func parseModelPricing(pricingData interface{}) (*ModelPricing, error) {
	mp := &ModelPricing{}

	switch v := pricingData.(type) {
	case []interface{}:
		// It's a list of tiers.
		for _, tierData := range v {
			tierMap, ok := tierData.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid pricing tier format")
			}
			tier := PricingTier{}
			if th, ok := tierMap["threshold"].(int); ok {
				tier.Threshold = th
			}
			if in, ok := tierMap["input"].(float64); ok {
				tier.Input = in
			} else if in, ok := tierMap["input"].(int); ok {
				tier.Input = float64(in)
			}
			if out, ok := tierMap["output"].(float64); ok {
				tier.Output = out
			} else if out, ok := tierMap["output"].(int); ok {
				tier.Output = float64(out)
			}
			mp.Tiers = append(mp.Tiers, tier)
		}
	case map[string]interface{}:
		// It's a simple price, has overrides, or already processed tiers.

		// Check if it has already-processed tiers (from config merging)
		if tiers, ok := v["tiers"].([]interface{}); ok {
			for _, tierData := range tiers {
				tierMap, ok := tierData.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("invalid pricing tier format in tiers array")
				}
				tier := PricingTier{}
				if th, ok := tierMap["threshold"].(int); ok {
					tier.Threshold = th
				} else if th, ok := tierMap["threshold"].(float64); ok {
					tier.Threshold = int(th)
				}
				if in, ok := tierMap["input"].(float64); ok {
					tier.Input = in
				} else if in, ok := tierMap["input"].(int); ok {
					tier.Input = float64(in)
				}
				if out, ok := tierMap["output"].(float64); ok {
					tier.Output = out
				} else if out, ok := tierMap["output"].(int); ok {
					tier.Output = float64(out)
				}
				mp.Tiers = append(mp.Tiers, tier)
			}
		} else if _, ok := v["input"]; ok {
			// Simple pricing.
			tier := PricingTier{Threshold: 0}
			if in, ok := v["input"].(float64); ok {
				tier.Input = in
			} else if in, ok := v["input"].(int); ok {
				tier.Input = float64(in)
			}
			if out, ok := v["output"].(float64); ok {
				tier.Output = out
			} else if out, ok := v["output"].(int); ok {
				tier.Output = float64(out)
			}
			mp.Tiers = []PricingTier{tier}
		}

		if overrides, ok := v["overrides"].(map[string]interface{}); ok {
			mp.Overrides = make(map[string]Pricing)
			for alias, overrideData := range overrides {
				overrideMap := overrideData.(map[string]interface{})
				pricing := Pricing{}
				if in, ok := overrideMap["input"].(float64); ok {
					pricing.Input = in
				} else if in, ok := overrideMap["input"].(int); ok {
					pricing.Input = float64(in)
				}
				if out, ok := overrideMap["output"].(float64); ok {
					pricing.Output = out
				} else if out, ok := overrideMap["output"].(int); ok {
					pricing.Output = float64(out)
				}
				mp.Overrides[alias] = pricing
			}
		}
	default:
		return nil, fmt.Errorf("unsupported pricing format: %T", pricingData)
	}

	return mp, nil
}

// GetModelConfig returns the model configuration for a provider and model name,
// resolving aliases to the canonical model entry.
func (c *YAMLConfig) GetModelConfig(provider, model string) (*ModelConfig, string) {
	if c == nil || model == "" {
		return nil, ""
	}
	providerConfig, exists := c.Providers[provider]
	if !exists {
		return nil, ""
	}
	if mc, ok := providerConfig.Models[model]; ok {
		return &mc, model
	}
	for canonicalName, mc := range providerConfig.Models {
		for _, alias := range mc.Aliases {
			if alias == model {
				return &mc, canonicalName
			}
		}
	}
	return nil, ""
}

// LookupRetiredModel returns retirement metadata when the provider/model slug
// is listed in retired_models (canonical name or alias).
func (c *YAMLConfig) LookupRetiredModel(provider, model string) (RetiredModelEntry, bool) {
	if c == nil || model == "" {
		return RetiredModelEntry{}, false
	}
	providerModels, ok := c.RetiredModels[provider]
	if !ok {
		return RetiredModelEntry{}, false
	}
	if entry, ok := providerModels[model]; ok {
		return entry, true
	}
	for _, entry := range providerModels {
		for _, alias := range entry.Aliases {
			if alias == model {
				return entry, true
			}
		}
	}
	return RetiredModelEntry{}, false
}

// GetModelPricing returns the pricing information for a specific provider and model
func (c *YAMLConfig) GetModelPricing(provider, model string, inputTokens int) (*Pricing, error) {
	providerConfig, exists := c.Providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	if !providerConfig.Enabled {
		return nil, fmt.Errorf("provider %s is disabled", provider)
	}

	// findModelConfig checks for a direct match or an alias and returns the
	// canonical model's configuration and its name.
	findModelConfig := func(modelName string) (*ModelConfig, string) {
		// Check for a direct match first.
		if mc, ok := providerConfig.Models[modelName]; ok {
			return &mc, modelName
		}
		// Check if the model is an alias.
		for canonicalName, mc := range providerConfig.Models {
			for _, alias := range mc.Aliases {
				if alias == modelName {
					return &mc, canonicalName
				}
			}
		}
		return nil, ""
	}

	modelConfig, canonicalName := findModelConfig(model)
	if modelConfig == nil || !modelConfig.Enabled {
		return nil, fmt.Errorf("model %s for provider %s is not configured or disabled", model, provider)
	}

	modelPricing, ok := modelConfig.Pricing.(*ModelPricing)
	if !ok || modelPricing == nil {
		return nil, fmt.Errorf("no pricing configured for provider %s model %s", provider, canonicalName)
	}

	// Check for a pricing override for the specific alias that was requested.
	if price, ok := modelPricing.Overrides[model]; ok {
		return &price, nil
	}

	// Handle tiered pricing.
	if len(modelPricing.Tiers) > 0 {
		// Sort tiers by threshold descending to find the correct tier.
		// A simple bubble sort is fine for the small number of tiers we expect.
		for i := 0; i < len(modelPricing.Tiers); i++ {
			for j := i + 1; j < len(modelPricing.Tiers); j++ {
				if modelPricing.Tiers[i].Threshold < modelPricing.Tiers[j].Threshold {
					modelPricing.Tiers[i], modelPricing.Tiers[j] = modelPricing.Tiers[j], modelPricing.Tiers[i]
				}
			}
		}

		for _, tier := range modelPricing.Tiers {
			if tier.Threshold == 0 || inputTokens <= tier.Threshold {
				return &Pricing{Input: tier.Input, Output: tier.Output}, nil
			}
		}
	}

	return nil, fmt.Errorf("no applicable pricing tier found for provider %s model %s with %d tokens", provider, canonicalName, inputTokens)
}

// GetDefaultYAMLConfig returns a default configuration
func GetDefaultYAMLConfig() *YAMLConfig {
	return &YAMLConfig{
		Enabled: true,
		Features: FeaturesConfig{
			CircuitBreaker: CircuitBreakerConfig{
				Enabled:                         false,
				Mode:                            "log",
				Backend:                         "memory",
				FailureThreshold:                5,
				WindowSeconds:                   120,
				CooldownSeconds:                 300,
				MaxTransientRetries:             2,
				MaxRateLimitRetries:             2,
				RetryContributionMode:           "log",
				GlobalRateLimitEscalationWindow: 60,
				TestModeEnabled:                 false,
			},
			CostTracking: CostTrackingConfig{
				Enabled: true,
				Transports: []TransportConfig{
					{
						Type: "file",
						File: &FileTransportConfig{
							Path: "./cost_tracking.json",
						},
					},
				},
			},
			RateLimiting: RateLimitingConfig{
				Enabled: false,
				Backend: "memory",
				Limits: LimitsConfig{
					RequestsPerMinute: 0,
					TokensPerMinute:   0,
					RequestsPerDay:    0,
					TokensPerDay:      0,
				},
				Estimation: EstimationConfig{
					MaxSampleBytes: 20000,
					BytesPerToken:  4,
					CharsPerToken:  4,
					ProviderCharsPerToken: map[string]int{
						"openai":    5,
						"anthropic": 3,
					},
				},
			},
		},
		Providers: map[string]ProviderConfig{
			"openai": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
			},
			"anthropic": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
			},
			"gemini": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
			},
		},
	}
}

// LogConfiguration logs the configuration in a clean, per-model format
func (c *YAMLConfig) LogConfiguration(logger *slog.Logger) {
	if !c.Enabled {
		logger.Warn("Configuration is disabled")
		return
	}

	logger.Info("📋 Configuration Summary:", "enabled", c.Enabled, "providers_configured", len(c.Providers))

	// Log provider and model details
	for providerName, provider := range c.Providers {
		if !provider.Enabled {
			logger.Info("Provider disabled", "provider", strings.ToUpper(providerName))
			continue
		}

		logger.Info("Provider enabled", "provider", strings.ToUpper(providerName))

		// Log model-specific configurations
		if len(provider.Models) > 0 {
			logger.Info("Models configured", "provider", providerName, "count", len(provider.Models))
			for modelName, model := range provider.Models {
				status := "ENABLED"
				if !model.Enabled {
					status = "DISABLED"
				}

				logger.Info("Model status", "provider", providerName, "model", modelName, "status", status)

				// Log aliases if they exist
				if len(model.Aliases) > 0 {
					logger.Info("Model aliases", "provider", providerName, "model", modelName, "aliases", strings.Join(model.Aliases, ", "))
				}

				// Log pricing information for enabled models
				if model.Enabled && model.Pricing != nil {
					modelPricing, ok := model.Pricing.(*ModelPricing)
					if !ok {
						logger.Warn("Could not parse pricing info", "provider", providerName, "model", modelName)
						continue
					}
					if len(modelPricing.Tiers) > 1 {
						for _, tier := range modelPricing.Tiers {
							if tier.Threshold > 0 {
								logger.Info("Tiered pricing", "provider", providerName, "model", modelName, "threshold", tier.Threshold, "input_cost", tier.Input, "output_cost", tier.Output)
							} else {
								logger.Info("Fallback pricing", "provider", providerName, "model", modelName, "input_cost", tier.Input, "output_cost", tier.Output)
							}
						}
					} else if len(modelPricing.Tiers) == 1 {
						logger.Info("Simple pricing", "provider", providerName, "model", modelName, "input_cost", modelPricing.Tiers[0].Input, "output_cost", modelPricing.Tiers[0].Output)
					}

					if len(modelPricing.Overrides) > 0 {
						for alias, price := range modelPricing.Overrides {
							logger.Info("Pricing override", "provider", providerName, "model", modelName, "alias", alias, "input_cost", price.Input, "output_cost", price.Output)
						}
					}
				} else if model.Enabled {
					logger.Info("Pricing not configured", "provider", providerName, "model", modelName)
				}
			}
		} else {
			logger.Info("No models specifically configured", "provider", providerName)
		}
	}
}
