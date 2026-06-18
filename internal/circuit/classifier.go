package circuit

import (
	"bytes"
	"context"
	"encoding/json"
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
		//   - rate_limit_reached: transient; headers always present
		//   - insufficient_quota: billing cap exhausted; no headers, body has code="insufficient_quota"
		//
		// Primary check: read the JSON error code from the body (authoritative).
		// Secondary check: if the body is unreadable / unparseable, fall back to
		// the missing-header heuristic as a best-effort defence.
		//
		// insufficient_quota is a billing cap, not a transient throttle: classify
		// it separately so the transport passes the real error through instead of
		// retrying and masking it behind a synthetic rate-limit 429.
		if peekOpenAIErrorCode(resp) == "insufficient_quota" {
			return FailureClassInsufficientQuota
		}
		rr := resp.Header.Get("x-ratelimit-remaining-requests")
		rt := resp.Header.Get("x-ratelimit-remaining-tokens")
		ra := resp.Header.Get("retry-after")
		if rr == "" && rt == "" && ra == "" {
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

// responseBodyPeekBytes is how much of an upstream error body we read for
// classification and observability.  Kept small so we never buffer a full
// multi-megabyte error page into memory on the hot path.
const responseBodyPeekBytes = 512

type geminiErrorEnvelope struct {
	Error struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthropicErrorEnvelope struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type openAIErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// peekOpenAIErrorCode reads up to responseBodyPeekBytes from resp.Body,
// extracts the top-level error.code field, and restores the body so
// downstream consumers still see the full response.  Returns "" on any read
// or parse failure (fail-open — caller falls back to header heuristics).
func peekOpenAIErrorCode(resp *http.Response) string {
	return peekOpenAIErrorField(resp, func(e openAIErrorEnvelope) string { return e.Error.Code })
}

func peekOpenAIErrorField(resp *http.Response, pick func(openAIErrorEnvelope) string) string {
	buf := peekResponseBodyPrefix(resp)
	if len(buf) == 0 {
		return ""
	}
	var envelope openAIErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return ""
	}
	return pick(envelope)
}

func peekGeminiErrorStatus(resp *http.Response) string {
	return peekGeminiErrorField(resp, func(e geminiErrorEnvelope) string { return e.Error.Status })
}

func peekGeminiErrorField(resp *http.Response, pick func(geminiErrorEnvelope) string) string {
	buf := peekResponseBodyPrefix(resp)
	if len(buf) == 0 {
		return ""
	}
	var envelope geminiErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return ""
	}
	return pick(envelope)
}

func peekAnthropicErrorType(resp *http.Response) string {
	buf := peekResponseBodyPrefix(resp)
	if len(buf) == 0 {
		return ""
	}
	var envelope anthropicErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return ""
	}
	return envelope.Error.Type
}

func gemini429IsQuota(resp *http.Response) bool {
	buf := peekResponseBodyPrefix(resp)
	if len(buf) == 0 {
		return false
	}
	var envelope geminiErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return false
	}
	if envelope.Error.Status != "RESOURCE_EXHAUSTED" {
		return false
	}
	msg := strings.ToLower(envelope.Error.Message)
	return strings.Contains(msg, "quota")
}

// peekResponseBodyPrefix reads up to responseBodyPeekBytes from resp.Body
// and restores the stream so callers can still forward or drain the full
// body afterward.  Returns nil when resp / Body is nil.
func peekResponseBodyPrefix(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}
	orig := resp.Body
	buf, _ := io.ReadAll(io.LimitReader(orig, responseBodyPeekBytes))
	// Preserve the original Closer so closing resp.Body still releases the
	// upstream connection (io.NopCloser would swallow Close and leak it).
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(bytes.NewReader(buf), orig),
		Closer: orig,
	}
	return buf
}

// peekUpstreamErrorDetail extracts a compact, provider-aware summary from
// an upstream error response body for circuit observability logs.  Safe to
// call before drainResponseBody; the body is restored for subsequent reads.
func peekUpstreamErrorDetail(provider string, resp *http.Response) string {
	buf := peekResponseBodyPrefix(resp)
	if len(buf) == 0 {
		return ""
	}
	return formatUpstreamErrorDetail(provider, buf)
}

func formatUpstreamErrorDetail(provider string, buf []byte) string {
	switch provider {
	case "gemini":
		return formatGeminiErrorDetail(buf)
	case "openai":
		return formatOpenAIErrorDetail(buf)
	case "anthropic":
		return formatAnthropicErrorDetail(buf)
	default:
		return formatGenericErrorDetail(buf)
	}
}

func formatGeminiErrorDetail(buf []byte) string {
	var envelope geminiErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return formatGenericErrorDetail(buf)
	}
	return joinUpstreamErrorParts(envelope.Error.Status, envelope.Error.Message)
}

func formatOpenAIErrorDetail(buf []byte) string {
	var envelope openAIErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return formatGenericErrorDetail(buf)
	}
	kind := envelope.Error.Code
	if kind == "" {
		kind = envelope.Error.Type
	}
	return joinUpstreamErrorParts(kind, envelope.Error.Message)
}

func formatAnthropicErrorDetail(buf []byte) string {
	var envelope anthropicErrorEnvelope
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return formatGenericErrorDetail(buf)
	}
	return joinUpstreamErrorParts(envelope.Error.Type, envelope.Error.Message)
}

func formatGenericErrorDetail(buf []byte) string {
	s := strings.TrimSpace(string(buf))
	if s == "" {
		return ""
	}
	if len(s) > maxErrorStringLength {
		return s[:maxErrorStringLength] + "...(truncated)"
	}
	return s
}

func joinUpstreamErrorParts(kind, message string) string {
	kind = strings.TrimSpace(kind)
	message = strings.TrimSpace(message)
	switch {
	case kind != "" && message != "":
		return truncateString(kind + ": " + message)
	case kind != "":
		return truncateString(kind)
	case message != "":
		return truncateString(message)
	default:
		return ""
	}
}

// truncateString bounds free-form upstream error text for slog attributes.
func truncateString(s string) string {
	if s == "" {
		return ""
	}
	if len(s) > maxErrorStringLength {
		return s[:maxErrorStringLength] + "...(truncated)"
	}
	return s
}

func classifyGemini(status int, resp *http.Response) FailureClass {
	switch status {
	case 500, 503:
		return FailureClassDegraded
	case 504: // DEADLINE_EXCEEDED
		return FailureClassDegraded
	case 429: // RESOURCE_EXHAUSTED — quota vs transient rate limit
		if gemini429IsQuota(resp) {
			return FailureClassGlobalRateLimit
		}
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
	KindHTTP429Quota      FailureKind = "http_429_quota"
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

	// Provider-specific kinds parsed from upstream JSON error bodies.
	// Gemini: https://ai.google.dev/gemini-api/docs/troubleshooting
	KindGeminiUnavailable       FailureKind = "gemini_unavailable"
	KindGeminiResourceExhausted FailureKind = "gemini_resource_exhausted"
	KindGeminiInternal          FailureKind = "gemini_internal"
	KindGeminiDeadlineExceeded  FailureKind = "gemini_deadline_exceeded"

	// Anthropic: https://docs.anthropic.com/en/api/errors
	KindAnthropicOverloaded      FailureKind = "anthropic_overloaded"
	KindAnthropicRateLimit       FailureKind = "anthropic_rate_limit"
	KindAnthropicAPIError        FailureKind = "anthropic_api_error"
	KindAnthropicTimeout         FailureKind = "anthropic_timeout"
	KindAnthropicRequestTooLarge FailureKind = "anthropic_request_too_large"

	KindOpenAIInsufficientQuota FailureKind = "openai_insufficient_quota"
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
	base := classifyHTTPFailureKind(provider, resp, err)
	return refineProviderFailureKind(provider, status, resp, base)
}

func classifyHTTPFailureKind(provider string, resp *http.Response, err error) FailureKind {
	status := resp.StatusCode
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
		fc := ClassifyResponse(provider, resp, err)
		switch fc {
		case FailureClassInsufficientQuota:
			return KindHTTP429Quota
		case FailureClassGlobalRateLimit:
			return KindHTTP429Global
		default:
			return KindHTTP429Local
		}
	}
	if status >= 500 {
		return KindHTTP5xxOther
	}
	return KindHTTP4xx
}

func refineProviderFailureKind(provider string, status int, resp *http.Response, base FailureKind) FailureKind {
	switch provider {
	case "gemini":
		return refineGeminiFailureKind(status, resp, base)
	case "anthropic":
		return refineAnthropicFailureKind(status, resp, base)
	case "openai":
		return refineOpenAIFailureKind(status, resp, base)
	default:
		return base
	}
}

func refineGeminiFailureKind(status int, resp *http.Response, base FailureKind) FailureKind {
	switch status {
	case 503:
		if peekGeminiErrorStatus(resp) == "UNAVAILABLE" {
			return KindGeminiUnavailable
		}
	case 429:
		if peekGeminiErrorStatus(resp) == "RESOURCE_EXHAUSTED" {
			return KindGeminiResourceExhausted
		}
	case 500:
		if peekGeminiErrorStatus(resp) == "INTERNAL" {
			return KindGeminiInternal
		}
	case 504:
		if peekGeminiErrorStatus(resp) == "DEADLINE_EXCEEDED" {
			return KindGeminiDeadlineExceeded
		}
	}
	return base
}

func refineAnthropicFailureKind(status int, resp *http.Response, base FailureKind) FailureKind {
	errType := peekAnthropicErrorType(resp)
	switch status {
	case 529:
		if errType == "overloaded_error" || errType == "" {
			return KindAnthropicOverloaded
		}
	case 429:
		if errType == "rate_limit_error" {
			return KindAnthropicRateLimit
		}
	case 500:
		if errType == "api_error" {
			return KindAnthropicAPIError
		}
	case 504:
		if errType == "timeout_error" {
			return KindAnthropicTimeout
		}
	case 413:
		if errType == "request_too_large" {
			return KindAnthropicRequestTooLarge
		}
	}
	return base
}

func refineOpenAIFailureKind(status int, resp *http.Response, base FailureKind) FailureKind {
	if status == 429 && peekOpenAIErrorCode(resp) == "insufficient_quota" {
		return KindOpenAIInsufficientQuota
	}
	return base
}

// upstreamDetailCode extracts the provider error code/type prefix from a
// compact upstream_error log value ("CODE: message" → "CODE").
func upstreamDetailCode(detail string) string {
	idx := strings.Index(detail, ":")
	if idx <= 0 {
		return strings.TrimSpace(detail)
	}
	return strings.TrimSpace(detail[:idx])
}

// refineFailureKindFromUpstreamDetail maps a captured upstream_error string
// back to a provider-specific FailureKind when the response body has already
// been drained and ClassifyFailureKind can no longer peek it.
func refineFailureKindFromUpstreamDetail(provider string, detail string, base FailureKind) FailureKind {
	code := upstreamDetailCode(detail)
	switch provider {
	case "gemini":
		switch code {
		case "UNAVAILABLE":
			return KindGeminiUnavailable
		case "RESOURCE_EXHAUSTED":
			return KindGeminiResourceExhausted
		case "INTERNAL":
			return KindGeminiInternal
		case "DEADLINE_EXCEEDED":
			return KindGeminiDeadlineExceeded
		}
	case "anthropic":
		switch code {
		case "overloaded_error":
			return KindAnthropicOverloaded
		case "rate_limit_error":
			return KindAnthropicRateLimit
		case "api_error":
			return KindAnthropicAPIError
		case "timeout_error":
			return KindAnthropicTimeout
		case "request_too_large":
			return KindAnthropicRequestTooLarge
		}
	case "openai":
		if code == "insufficient_quota" {
			return KindOpenAIInsufficientQuota
		}
	}
	return base
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
