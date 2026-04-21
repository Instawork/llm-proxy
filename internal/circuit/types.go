package circuit

import "time"

// MagicString is injected into every degraded error response body so that
// downstream callers (e.g. Finch Python SDKs) can detect provider degradation
// by checking `"[FINCH_PROVIDER_DEGRADED]" in str(e)` regardless of which SDK
// exception type wraps the HTTP error.
const MagicString = "[FINCH_PROVIDER_DEGRADED]"

// TestModeHeader is the HTTP request header used to force specific failure
// scenarios in integration tests without hitting real providers.
const TestModeHeader = "X-LLM-Proxy-Test-Mode"

// TestModeQueryParam is the URL query parameter equivalent of TestModeHeader.
// SDKs like the Google Gemini client that don't support custom HTTP headers can
// embed the test mode value directly in the base URL instead:
//
//	base_url = "http://localhost:9002/gemini?llm_proxy_test_mode=force_degraded"
const TestModeQueryParam = "llm_proxy_test_mode"

// Test mode header values.
const (
	// TestModeForceDegraded bypasses the provider and returns a degraded error
	// immediately, simulating an open circuit or terminal provider failure.
	TestModeForceDegraded = "force_degraded"

	// TestModeForceTransientRecover simulates a 503 on the first attempt that
	// recovers on the proxy's internal retry, proving the retry loop works
	// without opening the circuit.
	TestModeForceTransientRecover = "force_transient_recover"
)

// State represents the current circuit breaker state for a provider.
type State int8

const (
	// StateClosed is the healthy state: requests flow through normally and the
	// proxy performs its standard transient retries.
	StateClosed State = iota

	// StateOpen is the degraded/cooldown state: the circuit has tripped.  The
	// proxy fast-fails every request without hitting the network and returns
	// the MagicString degraded error to Finch.
	StateOpen

	// StateHalfOpen is the probing state: the cooldown period has expired and
	// exactly one request is allowed through as a probe.  No retries.  If it
	// succeeds the circuit closes; if it fails the circuit re-opens for another
	// cooldown period.
	StateHalfOpen
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// FailureClass categorises an upstream failure so the transport can apply the
// right retry policy and decide whether to credit the circuit breaker.
type FailureClass string

const (
	// FailureClassLocalRateLimit is a model- or bucket-specific throttle (e.g.
	// a 429 that applies only to one model tier).  The proxy backs off and
	// retries but does NOT increment the provider degradation counter.
	FailureClassLocalRateLimit FailureClass = "localized_rate_limit"

	// FailureClassGlobalRateLimit is a provider-wide quota exhaustion (e.g. the
	// account has hit its global RPM/TPM limit).  The proxy backs off and
	// retries; if it persists beyond the escalation window it is promoted to
	// FailureClassDegraded.
	FailureClassGlobalRateLimit FailureClass = "global_rate_limit"

	// FailureClassDegraded represents a true provider outage or capacity
	// failure: 5xx responses (including Anthropic 529), network-level errors
	// (timeouts, connection resets, unexpected EOFs), or sustained global rate
	// limits.  Terminal failures of this class increment the circuit breaker
	// counter and can open the circuit.
	FailureClassDegraded FailureClass = "provider_degraded"

	// FailureClassNone signals that the response was successful or that the
	// failure type is unknown/not classifiable as transient.
	FailureClassNone FailureClass = ""
)

// Config holds all circuit-breaker and retry tuning parameters.  Zero values
// are replaced by the defaults documented on each field.
type Config struct {
	// Enabled gates the entire circuit-breaker feature.  When false the
	// transport behaves as a simple pass-through with no retries.
	Enabled bool

	// Backend selects the state store: "memory" (default) or "redis".
	Backend string

	// FailureThreshold is the number of terminal failures within WindowSeconds
	// required to open the circuit.  Default: 5.
	FailureThreshold int

	// WindowSeconds is the sliding-window TTL for failure counters.  Failures
	// older than this are discarded and do not contribute to the threshold.
	// Default: 120 (2 minutes).
	WindowSeconds int

	// CooldownSeconds is how long the circuit stays Open before transitioning
	// to Half-Open for a probe.  Default: 300 (5 minutes).
	CooldownSeconds int

	// MaxTransientRetries is the maximum number of retries for degraded-class
	// failures before declaring the request terminal.  Default: 2.
	MaxTransientRetries int

	// MaxRateLimitRetries is the maximum number of retries for rate-limit
	// failures before surfacing the error.  Default: 2.
	MaxRateLimitRetries int

	// RetryContributionMode controls whether retried failures count toward the
	// circuit-breaker threshold.
	//   "off"  – retried failures never contribute
	//   "log"  – retried failures are logged but do not contribute (default)
	//   "on"   – retried failures contribute to the threshold
	RetryContributionMode string

	// GlobalRateLimitEscalationWindow is how many seconds of sustained global
	// rate-limit failures must elapse before they escalate to provider_degraded.
	// Default: 60.
	GlobalRateLimitEscalationWindow int

	// Redis connection settings, used when Backend == "redis".
	RedisAddress  string
	RedisPassword string
	RedisDB       int
}

// Defaults returns a Config with all zero fields replaced by sensible defaults.
func (c Config) Defaults() Config {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 120
	}
	if c.CooldownSeconds <= 0 {
		c.CooldownSeconds = 300
	}
	if c.MaxTransientRetries <= 0 {
		c.MaxTransientRetries = 2
	}
	if c.MaxRateLimitRetries <= 0 {
		c.MaxRateLimitRetries = 2
	}
	if c.RetryContributionMode == "" {
		c.RetryContributionMode = "log"
	}
	if c.GlobalRateLimitEscalationWindow <= 0 {
		c.GlobalRateLimitEscalationWindow = 60
	}
	return c
}

// ProviderStats is returned by the Store for health / observability endpoints.
type ProviderStats struct {
	State         State
	Failures      int
	CooldownUntil *time.Time
}
