package circuit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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
		return classifyRateLimitScope(
			resp.Header.Get("x-ratelimit-remaining-requests"),
			resp.Header.Get("x-ratelimit-remaining-tokens"),
			resp.Header.Get("retry-after"),
		)
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

// parseRetryAfterSeconds returns the numeric seconds from a Retry-After header
// value.  Returns 0 if unparseable.
func parseRetryAfterSeconds(s string) int {
	s = strings.TrimSpace(s)
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	return 0
}
