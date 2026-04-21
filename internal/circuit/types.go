package circuit

import "time"

// DefaultDegradedSignal is the default marker embedded in synthetic degraded
// error response bodies.  Callers can override it per-deployment via
// Config.DegradedSignal; e.g. an operator that wants a provider-neutral tag
// might set it to "[LLM_PROXY_PROVIDER_DEGRADED]" or an org-specific string.
//
// ─── Why a body marker instead of a status code? ────────────────────────────
//
// The proxy already sets HTTP 503 and an X-Llm-Proxy-Error-Class response
// header on every synthesised degradation response, but neither is sufficient
// on its own for downstream clients to reliably detect a proxy-originated
// degradation:
//
//  1. **5xx is ambiguous.**  The proxy streams real 5xx responses straight
//     from providers (Anthropic 529, OpenAI 500/502/503/504, Gemini 500/503,
//     etc.) through unmodified.  A client that only looks at status code
//     cannot tell the difference between a passthrough upstream 503 and a
//     proxy-synthesised "circuit open" 503.  They have very different
//     semantics for retry / fallback decisions.
//
//  2. **4xx is wrong.**  The caller has done nothing wrong — the upstream is
//     degraded.  Using 4xx would break SDK retry logic that (correctly)
//     treats 4xx as a client-side error and refuses to retry.
//
//  3. **A novel status code (e.g. 599) is hostile to the ecosystem.**  Many
//     reverse proxies, CDNs, and HTTP clients coerce unknown status codes
//     to 500 or strip them entirely.  Anthropic / OpenAI / Google SDKs all
//     map any ≥500 response into a generic APIError / ServerError class.
//
//  4. **Custom headers get stripped by the SDK exception layer.**  By the
//     time an HTTPS error propagates up through e.g. the OpenAI Python SDK
//     or LangChain, the caller typically only sees `str(exception)` — the
//     response body in the exception message.  Response headers are usually
//     only accessible if the caller catches a specific, provider-native
//     exception type *before* any framework wraps it.  A substring of the
//     body, on the other hand, survives every wrapping layer because the
//     body ends up in the exception's message.
//
// So the contract is:
//   - HTTP 503 + `X-Llm-Proxy-Error-Class: provider_degraded` for clients
//     that read response metadata directly.
//   - DegradedSignal substring in the JSON error message for SDK / framework
//     consumers that only see the exception body.
//
// Clients should treat the presence of DegradedSignal anywhere in the
// exception / response body as authoritative.
const DefaultDegradedSignal = "[LLM_PROXY_PROVIDER_DEGRADED]"

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
	// a synthetic degraded error (503 + DegradedSignal in the body) to the
	// caller.
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

	// DegradedSignal is the opaque substring embedded in every synthesised
	// degraded error body so downstream clients can detect proxy-originated
	// provider degradation even after SDK / framework exception wrapping
	// discards headers and remaps status codes.  See DefaultDegradedSignal
	// for the rationale on why a body marker is used in addition to the 503
	// status and X-Llm-Proxy-Error-Class header.
	//
	// Defaults to DefaultDegradedSignal when empty.
	DegradedSignal string
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
	if c.DegradedSignal == "" {
		c.DegradedSignal = DefaultDegradedSignal
	}
	return c
}

// ProviderStats is returned by the Store for health / observability endpoints.
type ProviderStats struct {
	State         State
	Failures      int
	CooldownUntil *time.Time
}
