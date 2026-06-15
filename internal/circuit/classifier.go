package circuit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

// ClassifyResponse examines an HTTP response and the optional transport-level
// error to determine what kind of failure it represents.
//
// provider must be one of "openai", "anthropic", or "gemini".  When the
// response is nil (transport error) the network-level error is classified
// directly.
//
// Returns FailureClassNone for successful responses and for client errors that
// are not provider-side issues (4xx other than specific overload codes).
func ClassifyResponse(provider string, resp *http.Response, err error) FailureClass {
	// 1. Network-level errors: the provider never sent a clean HTTP response.
	if err != nil {
		return classifyNetworkError(err)
	}
	if resp == nil {
		return FailureClassDegraded
	}

	status := resp.StatusCode

	// 2. Success or informational — nothing to classify.
	if status < 400 {
		return FailureClassNone
	}

	// 3. Provider-specific classification.
	switch provider {
	case "anthropic":
		return classifyAnthropic(status, resp)
	case "openai":
		return classifyOpenAI(status, resp)
	case "gemini":
		return classifyGemini(status, resp)
	default:
		return classifyGeneric(status)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Provider-specific classifiers
// ─────────────────────────────────────────────────────────────────────────────

func classifyAnthropic(status int, resp *http.Response) FailureClass {
	switch status {
	case 529: // Anthropic "Overloaded" — capacity constraint, not a client error
		return FailureClassDegraded
	case 500, 502, 503, 504:
		return FailureClassDegraded
	case 429:
		return classifyRateLimitScope(
			resp.Header.Get("anthropic-ratelimit-requests-remaining"),
			resp.Header.Get("anthropic-ratelimit-tokens-remaining"),
			resp.Header.Get("retry-after"),
		)
	case 401, 403:
		// Auth errors are almost always client config issues, not provider
		// incidents.  Mark as unclassifiable (will surface as-is).
		return FailureClassNone
	default:
		if status >= 500 {
			return FailureClassDegraded
		}
		return FailureClassNone
	}
}

func classifyOpenAI(status int, resp *http.Response) FailureClass {
	switch status {
	case 500, 502, 503, 504:
		return FailureClassDegraded
	case 429:
		// OpenAI uses 429 for two distinct conditions:
		//   - rate_limit_reached: transient; always includes x-ratelimit-* and/or Retry-After
		//   - insufficient_quota: billing cap exhausted; no ratelimit headers at all
		// Retrying insufficient_quota is wasteful and delays the caller from
		// falling back to another provider.  Treat it as GlobalRateLimit so the
		// circuit escalates promptly and Finch's fallback_model fires.
		rr := resp.Header.Get("x-ratelimit-remaining-requests")
		rt := resp.Header.Get("x-ratelimit-remaining-tokens")
		ra := resp.Header.Get("retry-after")
		if rr == "" && rt == "" && ra == "" {
			// No ratelimit headers → insufficient_quota (billing exhausted).
			return FailureClassGlobalRateLimit
		}
		return classifyRateLimitScope(rr, rt, ra)
	case 401, 403:
		return FailureClassNone
	default:
		if status >= 500 {
			return FailureClassDegraded
		}
		return FailureClassNone
	}
}

func classifyGemini(status int, resp *http.Response) FailureClass {
	switch status {
	case 500, 503:
		return FailureClassDegraded
	case 504: // DEADLINE_EXCEEDED
		return FailureClassDegraded
	case 429: // RESOURCE_EXHAUSTED
		// Gemini does not expose granular rate-limit headers the way OpenAI
		// and Anthropic do, so we treat all 429s as localized for now and let
		// the escalation window promote them to global/degraded if needed.
		return FailureClassLocalRateLimit
	case 401, 403:
		return FailureClassNone
	default:
		if status >= 500 {
			return FailureClassDegraded
		}
		return FailureClassNone
	}
}

func classifyGeneric(status int) FailureClass {
	if status >= 500 {
		return FailureClassDegraded
	}
	return FailureClassNone
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// classifyRateLimitScope decides between LocalRateLimit and GlobalRateLimit
// based on remaining capacity headers.  When remaining capacity is 0 across
// the board (requests AND tokens both exhausted) we treat it as global.
func classifyRateLimitScope(remainingRequests, remainingTokens, retryAfter string) FailureClass {
	rr := strings.TrimSpace(remainingRequests)
	rt := strings.TrimSpace(remainingTokens)

	requestsExhausted := rr == "0"
	tokensExhausted := rt == "0"

	// If the provider says both bucket types are at zero, it's a global
	// account-wide exhaustion rather than a single model/tier limit.
	if requestsExhausted && tokensExhausted {
		return FailureClassGlobalRateLimit
	}

	// A long Retry-After is also a signal of a broader throttle.
	if retryAfter != "" {
		secs := parseRetryAfterSeconds(retryAfter)
		if secs >= 60 {
			return FailureClassGlobalRateLimit
		}
	}

	return FailureClassLocalRateLimit
}

// classifyNetworkError maps Go transport errors to FailureClassDegraded.
// These indicate the provider never returned a response, which is always a
// provider-side (or network-side) issue that we must track.
func classifyNetworkError(err error) FailureClass {
	if err == nil {
		return FailureClassNone
	}

	// Client-initiated cancellation is not a provider failure and must never
	// count toward the circuit's failure window. We still treat a deadline
	// exceeded at the request level as a degradation signal below, since that
	// usually indicates the provider is too slow.
	if errors.Is(err, context.Canceled) {
		return FailureClassNone
	}

	// Unexpected EOF or closed connection.
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return FailureClassDegraded
	}

	// context deadline / timeout.
	if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return FailureClassDegraded
	}

	// net.Error covers dial timeout, connection refused, etc.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return FailureClassDegraded
	}

	// String-based heuristic for TLS / connection resets that don't always
	// surface as net.Error.
	msg := err.Error()
	for _, pattern := range []string{
		"connection reset",
		"connection refused",
		"tls handshake",
		"no such host",
		"i/o timeout",
		"context deadline exceeded",
		"EOF",
	} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(pattern)) {
			return FailureClassDegraded
		}
	}

	return FailureClassDegraded // unknown transport errors → degraded
}

// FailureKind labels the cause of a failure for log and metric attribution.
//
// FailureClass tells the breaker whether to count an event toward its
// threshold; FailureKind is a finer-grained string that survives into
// dashboards and alerts so operators can see *what* failed (a 504, a
// connection reset, a client deadline, etc.) without re-grepping logs.
//
// In particular, KindClientDeadline lets us split "Gemini was slow"
// from "the caller's httpx timeout was too tight": both classify as
// FailureClassDegraded today (so the breaker still trips on sustained
// client-side timeouts, which is usually the right call), but they are
// emitted with distinct kinds so a dashboard can subtract client-side
// timeout noise from the provider-degradation signal.
type FailureKind string

const (
	KindNone              FailureKind = ""
	KindHTTPOverloaded    FailureKind = "http_overloaded" // Anthropic 529
	KindHTTP500           FailureKind = "http_500"
	KindHTTP502           FailureKind = "http_502"
	KindHTTP503           FailureKind = "http_503"
	KindHTTP504           FailureKind = "http_504"
	KindHTTP5xxOther      FailureKind = "http_5xx_other"
	KindHTTP429Local      FailureKind = "http_429_local"
	KindHTTP429Global     FailureKind = "http_429_global"
	KindHTTP4xx           FailureKind = "http_4xx"
	KindClientCanceled    FailureKind = "client_canceled"
	KindClientDeadline    FailureKind = "client_deadline_exceeded"
	KindUnexpectedEOF     FailureKind = "unexpected_eof"
	KindEOF               FailureKind = "eof"
	KindConnectionReset   FailureKind = "connection_reset"
	KindConnectionRefused FailureKind = "connection_refused"
	KindTLSHandshake      FailureKind = "tls_handshake"
	KindDNSFailure        FailureKind = "dns_failure"
	KindIOTimeout         FailureKind = "io_timeout"
	KindNetworkOther      FailureKind = "network_other"
	KindNilResponse       FailureKind = "nil_response"
	KindUnknownTransport  FailureKind = "unknown_transport"

	// KindCircuitOpen marks proxy-synthesised events (fast-fail on an
	// open circuit, half-open probe slot already taken,
	// log-mode would_have_fast_failed) that did not correspond to an
	// actual upstream attempt.  Distinguishing it from KindNilResponse
	// — which still means "we tried and got nothing back" — keeps
	// dashboards honest: "we deliberately didn't try" is a different
	// failure mode from "the upstream gave us nothing".
	KindCircuitOpen FailureKind = "circuit_open"

	// KindBodyTooLarge marks the request body exceeding
	// Config.MaxRetryableBodyBytes.  This is a proxy-side guard, not
	// an upstream signal, so it gets its own kind to avoid showing up
	// next to provider-degradation events on dashboards.
	KindBodyTooLarge FailureKind = "body_too_large"
)

// ClassifyFailureKind returns a string label describing the cause of a
// failure, suitable for use as a log attribute (failure_kind) and a
// dogstatsd tag.  Returns KindNone for successful (or non-failure)
// responses.
//
// The classification is intentionally orthogonal to FailureClass: a
// caller that only cares whether to credit the breaker still uses
// ClassifyResponse; this function is for observability only.
func ClassifyFailureKind(provider string, resp *http.Response, err error) FailureKind {
	if err != nil {
		return classifyTransportErrorKind(err)
	}
	if resp == nil {
		return KindNilResponse
	}
	status := resp.StatusCode
	if status < 400 {
		return KindNone
	}
	switch status {
	case 500:
		return KindHTTP500
	case 502:
		return KindHTTP502
	case 503:
		return KindHTTP503
	case 504:
		return KindHTTP504
	case 529:
		return KindHTTPOverloaded
	case 429:
		// Mirror the LocalRateLimit/GlobalRateLimit split from
		// ClassifyResponse so dashboards can break 429s out from each
		// other without re-deriving the global/local bucket.
		fc := ClassifyResponse(provider, resp, err)
		if fc == FailureClassGlobalRateLimit {
			return KindHTTP429Global
		}
		return KindHTTP429Local
	}
	if status >= 500 {
		return KindHTTP5xxOther
	}
	return KindHTTP4xx
}

// classifyTransportErrorKind is the network-error sibling of
// ClassifyFailureKind.  It mirrors the branch order of classifyNetworkError
// but returns a string label instead of a FailureClass.
func classifyTransportErrorKind(err error) FailureKind {
	if err == nil {
		return KindNone
	}
	if errors.Is(err, context.Canceled) {
		return KindClientCanceled
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return KindUnexpectedEOF
	}
	if errors.Is(err, io.EOF) {
		return KindEOF
	}
	if errors.Is(err, http.ErrHandlerTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return KindClientDeadline
	}

	// Inspect the message for the well-known patterns first so a
	// connection-reset wrapped inside a *net.OpError surfaces as
	// connection_reset rather than network_other.  Same idea for the
	// other patterns: matching the string is more specific than the
	// net.Error branch.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection reset"):
		return KindConnectionReset
	case strings.Contains(msg, "connection refused"):
		return KindConnectionRefused
	case strings.Contains(msg, "tls handshake"):
		return KindTLSHandshake
	case strings.Contains(msg, "no such host"):
		return KindDNSFailure
	case strings.Contains(msg, "i/o timeout"):
		return KindIOTimeout
	case strings.Contains(msg, "context deadline exceeded"):
		return KindClientDeadline
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return KindIOTimeout
		}
		return KindNetworkOther
	}

	return KindUnknownTransport
}

// parseRetryAfterSeconds returns seconds from a Retry-After header value.
// Supports both delta-seconds and HTTP-date forms. Returns 0 if unparseable
// or if the parsed date is in the past.
func parseRetryAfterSeconds(s string) int {
	s = strings.TrimSpace(s)
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}

	t, err := http.ParseTime(s)
	if err != nil {
		return 0
	}
	seconds := t.Sub(time.Now()).Seconds()
	if seconds <= 0 {
		return 0
	}
	return int(math.Ceil(seconds))
}
