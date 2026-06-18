package circuit

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func resp(status int, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: http.NoBody}
}

func respWithBody(status int, headers map[string]string, body string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestClassifyResponse_Success(t *testing.T) {
	fc := ClassifyResponse("openai", resp(200, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("200 should be FailureClassNone, got %s", fc)
	}
}

// ─── Anthropic ─────────────────────────────────────────────────────────────

func TestClassify_Anthropic_529(t *testing.T) {
	fc := ClassifyResponse("anthropic", resp(529, nil), nil)
	if fc != FailureClassDegraded {
		t.Fatalf("Anthropic 529 should be Degraded, got %s", fc)
	}
}

func TestClassify_Anthropic_5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		fc := ClassifyResponse("anthropic", resp(code, nil), nil)
		if fc != FailureClassDegraded {
			t.Fatalf("Anthropic %d should be Degraded, got %s", code, fc)
		}
	}
}

func TestClassify_Anthropic_429_LocalRateLimit(t *testing.T) {
	// One bucket still has capacity.
	fc := ClassifyResponse("anthropic", resp(429, map[string]string{
		"anthropic-ratelimit-requests-remaining": "10",
		"anthropic-ratelimit-tokens-remaining":   "0",
	}), nil)
	if fc != FailureClassLocalRateLimit {
		t.Fatalf("want LocalRateLimit, got %s", fc)
	}
}

func TestClassify_Anthropic_429_GlobalRateLimit(t *testing.T) {
	// Both buckets exhausted.
	fc := ClassifyResponse("anthropic", resp(429, map[string]string{
		"anthropic-ratelimit-requests-remaining": "0",
		"anthropic-ratelimit-tokens-remaining":   "0",
	}), nil)
	if fc != FailureClassGlobalRateLimit {
		t.Fatalf("want GlobalRateLimit, got %s", fc)
	}
}

func TestClassify_Anthropic_401(t *testing.T) {
	fc := ClassifyResponse("anthropic", resp(401, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("Anthropic 401 should be FailureClassNone, got %s", fc)
	}
}

// ─── OpenAI ────────────────────────────────────────────────────────────────

func TestClassify_OpenAI_5xx(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		fc := ClassifyResponse("openai", resp(code, nil), nil)
		if fc != FailureClassDegraded {
			t.Fatalf("OpenAI %d should be Degraded, got %s", code, fc)
		}
	}
}

func TestClassify_OpenAI_429_Global(t *testing.T) {
	fc := ClassifyResponse("openai", resp(429, map[string]string{
		"x-ratelimit-remaining-requests": "0",
		"x-ratelimit-remaining-tokens":   "0",
	}), nil)
	if fc != FailureClassGlobalRateLimit {
		t.Fatalf("want GlobalRateLimit, got %s", fc)
	}
}

func TestClassify_OpenAI_429_InsufficientQuota_Body(t *testing.T) {
	// Primary path: JSON body contains error.code = "insufficient_quota".
	// This is the authoritative signal regardless of headers.
	body := `{"error":{"message":"You exceeded your current quota...","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`
	fc := ClassifyResponse("openai", respWithBody(429, nil, body), nil)
	if fc != FailureClassInsufficientQuota {
		t.Fatalf("OpenAI 429 with insufficient_quota body should be InsufficientQuota, got %s", fc)
	}
}

func TestClassify_OpenAI_429_InsufficientQuota_BodyRestoredAfterPeek(t *testing.T) {
	// Verify peekOpenAIErrorCode restores the body so the caller can still
	// read the full response after classification.
	body := `{"error":{"message":"You exceeded your current quota...","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`
	r := respWithBody(429, nil, body)
	ClassifyResponse("openai", r, nil)
	got, err := io.ReadAll(r.Body)
	if err != nil || string(got) != body {
		t.Fatalf("body not restored after peek: err=%v body=%q", err, got)
	}
}

type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (c *closeTrackingBody) Close() error { c.closed = true; return nil }

func TestClassify_OpenAI_429_PeekPreservesCloser(t *testing.T) {
	// The peeked body must keep the original Closer so closing resp.Body
	// still releases the upstream connection (io.NopCloser would leak it).
	body := `{"error":{"code":"insufficient_quota"}}`
	ctb := &closeTrackingBody{Reader: strings.NewReader(body)}
	r := &http.Response{StatusCode: 429, Header: make(http.Header), Body: ctb}
	ClassifyResponse("openai", r, nil)
	if err := r.Body.Close(); err != nil {
		t.Fatalf("close error: %v", err)
	}
	if !ctb.closed {
		t.Fatal("original body Close() was not propagated — connection would leak")
	}
}

func TestClassify_OpenAI_429_InsufficientQuota_FallbackHeuristic(t *testing.T) {
	// Fallback path: body is empty / unreadable, no ratelimit headers → heuristic.
	fc := ClassifyResponse("openai", resp(429, nil), nil)
	if fc != FailureClassGlobalRateLimit {
		t.Fatalf("OpenAI 429 with no body and no ratelimit headers should be GlobalRateLimit, got %s", fc)
	}
}

func TestClassify_OpenAI_429_Local(t *testing.T) {
	// Normal rate-limit with capacity remaining on one bucket → local.
	fc := ClassifyResponse("openai", resp(429, map[string]string{
		"x-ratelimit-remaining-requests": "10",
		"x-ratelimit-remaining-tokens":   "0",
	}), nil)
	if fc != FailureClassLocalRateLimit {
		t.Fatalf("want LocalRateLimit, got %s", fc)
	}
}

func TestClassify_OpenAI_429_LongRetryAfter(t *testing.T) {
	// Long Retry-After → global rate limit.
	fc := ClassifyResponse("openai", resp(429, map[string]string{
		"retry-after": "120",
	}), nil)
	if fc != FailureClassGlobalRateLimit {
		t.Fatalf("want GlobalRateLimit, got %s", fc)
	}
}

func TestClassify_OpenAI_429_HTTPDateRetryAfter(t *testing.T) {
	retryAt := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	fc := ClassifyResponse("openai", resp(429, map[string]string{
		"retry-after": retryAt,
	}), nil)
	if fc != FailureClassGlobalRateLimit {
		t.Fatalf("want GlobalRateLimit for future HTTP-date Retry-After, got %s", fc)
	}
}

func TestParseRetryAfterSeconds_PastHTTPDate(t *testing.T) {
	retryAt := time.Now().Add(-1 * time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfterSeconds(retryAt); got != 0 {
		t.Fatalf("past HTTP-date Retry-After should return 0, got %d", got)
	}
}

// ─── Gemini ────────────────────────────────────────────────────────────────

func TestClassify_Gemini_503(t *testing.T) {
	fc := ClassifyResponse("gemini", resp(503, nil), nil)
	if fc != FailureClassDegraded {
		t.Fatalf("Gemini 503 should be Degraded, got %s", fc)
	}
}

func TestPeekUpstreamErrorDetail_Gemini_UNAVAILABLE(t *testing.T) {
	body := `{"error":{"code":503,"message":"The model is overloaded. Please try again later.","status":"UNAVAILABLE"}}`
	r := respWithBody(503, nil, body)
	got := peekUpstreamErrorDetail("gemini", r)
	want := "UNAVAILABLE: The model is overloaded. Please try again later."
	if got != want {
		t.Fatalf("peekUpstreamErrorDetail(gemini) = %q, want %q", got, want)
	}
	// Body must remain readable after peek.
	rest, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(rest) != body {
		t.Fatalf("restored body = %q, want %q", string(rest), body)
	}
}

func TestPeekUpstreamErrorDetail_OpenAI_InsufficientQuota(t *testing.T) {
	body := `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`
	r := respWithBody(429, nil, body)
	got := peekUpstreamErrorDetail("openai", r)
	want := "insufficient_quota: You exceeded your current quota, please check your plan and billing details."
	if got != want {
		t.Fatalf("peekUpstreamErrorDetail(openai) = %q, want %q", got, want)
	}
}

func TestPeekUpstreamErrorDetail_Anthropic_Overloaded(t *testing.T) {
	body := `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	r := respWithBody(529, nil, body)
	got := peekUpstreamErrorDetail("anthropic", r)
	want := "overloaded_error: Overloaded"
	if got != want {
		t.Fatalf("peekUpstreamErrorDetail(anthropic) = %q, want %q", got, want)
	}
}

func TestFormatUpstreamErrorDetail_TruncatesLongMessage(t *testing.T) {
	longMsg := strings.Repeat("x", maxErrorStringLength+50)
	body := `{"error":{"status":"UNAVAILABLE","message":"` + longMsg + `"}}`
	got := formatUpstreamErrorDetail("gemini", []byte(body))
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("expected truncated suffix, got len=%d tail=%q", len(got), got[len(got)-20:])
	}
	if len(got) > maxErrorStringLength+len("...(truncated)") {
		t.Fatalf("truncated detail too long: %d", len(got))
	}
}

func TestClassify_Gemini_504_DeadlineExceeded(t *testing.T) {
	fc := ClassifyResponse("gemini", resp(504, nil), nil)
	if fc != FailureClassDegraded {
		t.Fatalf("Gemini 504 should be Degraded, got %s", fc)
	}
}

func TestClassify_Gemini_429_LocalRateLimit(t *testing.T) {
	fc := ClassifyResponse("gemini", resp(429, nil), nil)
	if fc != FailureClassLocalRateLimit {
		t.Fatalf("Gemini 429 should be LocalRateLimit, got %s", fc)
	}
}

// ─── Network errors ────────────────────────────────────────────────────────

func TestClassify_NetworkError_UnexpectedEOF(t *testing.T) {
	fc := ClassifyResponse("openai", nil, io.ErrUnexpectedEOF)
	if fc != FailureClassDegraded {
		t.Fatalf("unexpected EOF should be Degraded, got %s", fc)
	}
}

func TestClassify_NetworkError_Timeout(t *testing.T) {
	fc := ClassifyResponse("anthropic", nil, &net.OpError{
		Op:  "dial",
		Err: errors.New("i/o timeout"),
	})
	if fc != FailureClassDegraded {
		t.Fatalf("dial timeout should be Degraded, got %s", fc)
	}
}

func TestClassify_NetworkError_NilResponse(t *testing.T) {
	fc := ClassifyResponse("gemini", nil, errors.New("connection reset by peer"))
	if fc != FailureClassDegraded {
		t.Fatalf("nil response with error should be Degraded, got %s", fc)
	}
}

func TestClassify_NilResponse_NilError(t *testing.T) {
	fc := ClassifyResponse("openai", nil, nil)
	if fc != FailureClassDegraded {
		t.Fatalf("nil response + nil error should be Degraded, got %s", fc)
	}
}

func TestClassify_UnknownProvider_5xx(t *testing.T) {
	fc := ClassifyResponse("mistral", resp(503, nil), nil)
	if fc != FailureClassDegraded {
		t.Fatalf("unknown provider 503 should be Degraded via classifyGeneric, got %s", fc)
	}
}

func TestClassify_UnknownProvider_400(t *testing.T) {
	fc := ClassifyResponse("mistral", resp(400, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("unknown provider 400 should be None via classifyGeneric, got %s", fc)
	}
}

func TestClassify_OpenAI_401(t *testing.T) {
	fc := ClassifyResponse("openai", resp(401, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("OpenAI 401 should be FailureClassNone, got %s", fc)
	}
}

func TestClassify_OpenAI_403(t *testing.T) {
	fc := ClassifyResponse("openai", resp(403, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("OpenAI 403 should be FailureClassNone, got %s", fc)
	}
}

func TestClassify_Gemini_401(t *testing.T) {
	fc := ClassifyResponse("gemini", resp(401, nil), nil)
	if fc != FailureClassNone {
		t.Fatalf("Gemini 401 should be FailureClassNone, got %s", fc)
	}
}

func TestClassify_OpenAI_429_ShortRetryAfter_Local(t *testing.T) {
	fc := ClassifyResponse("openai", resp(429, map[string]string{
		"retry-after": "5",
	}), nil)
	if fc != FailureClassLocalRateLimit {
		t.Fatalf("OpenAI 429 with short Retry-After should be LocalRateLimit, got %s", fc)
	}
}

func TestClassify_Anthropic_429_NoHeaders_Local(t *testing.T) {
	fc := ClassifyResponse("anthropic", resp(429, nil), nil)
	if fc != FailureClassLocalRateLimit {
		t.Fatalf("Anthropic 429 with no remaining headers should be LocalRateLimit, got %s", fc)
	}
}

// TestClassifyResponse_DefaultBranches covers the default case in the
// Anthropic and Gemini per-provider classifiers: 5xx codes not explicitly
// listed in the switch (e.g. 599) should be Degraded, and non-auth 4xx codes
// (e.g. 418) should be None. These are not covered by the other Classify_*
// tests in this file.
func TestClassifyResponse_DefaultBranches(t *testing.T) {
	mkResp := func(status int) *http.Response {
		return &http.Response{StatusCode: status, Header: make(http.Header)}
	}

	assert.Equal(t, FailureClassDegraded, ClassifyResponse("anthropic", mkResp(599), nil))
	assert.Equal(t, FailureClassDegraded, ClassifyResponse("gemini", mkResp(599), nil))
	assert.Equal(t, FailureClassNone, ClassifyResponse("anthropic", mkResp(418), nil))
	assert.Equal(t, FailureClassNone, ClassifyResponse("gemini", mkResp(418), nil))
}

func TestRefineFailureKindFromUpstreamDetail(t *testing.T) {
	detail := "UNAVAILABLE: The model is overloaded."
	assert.Equal(t, KindGeminiUnavailable, refineFailureKindFromUpstreamDetail("gemini", detail, KindHTTP503))
	assert.Equal(t, KindAnthropicOverloaded, refineFailureKindFromUpstreamDetail("anthropic", "overloaded_error: Overloaded", KindHTTPOverloaded))
	assert.Equal(t, KindOpenAIInsufficientQuota, refineFailureKindFromUpstreamDetail("openai", "insufficient_quota: quota", KindHTTP429Quota))
}

func TestClassifyFailureKind_ProviderSpecific(t *testing.T) {
	geminiUnavailable := respWithBody(503, nil, `{"error":{"status":"UNAVAILABLE","message":"The model is overloaded."}}`)
	assert.Equal(t, KindGeminiUnavailable, ClassifyFailureKind("gemini", geminiUnavailable, nil))

	geminiQuota := respWithBody(429, nil, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"You exceeded your current quota, please check your plan."}}`)
	assert.Equal(t, KindGeminiResourceExhausted, ClassifyFailureKind("gemini", geminiQuota, nil))
	assert.Equal(t, FailureClassGlobalRateLimit, ClassifyResponse("gemini", geminiQuota, nil))

	geminiRateLimit := respWithBody(429, nil, `{"error":{"status":"RESOURCE_EXHAUSTED","message":"Too many requests per minute"}}`)
	assert.Equal(t, KindGeminiResourceExhausted, ClassifyFailureKind("gemini", geminiRateLimit, nil))
	assert.Equal(t, FailureClassLocalRateLimit, ClassifyResponse("gemini", geminiRateLimit, nil))

	anthropicOverloaded := respWithBody(529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	assert.Equal(t, KindAnthropicOverloaded, ClassifyFailureKind("anthropic", anthropicOverloaded, nil))

	anthropicRateLimit := respWithBody(429, map[string]string{
		"anthropic-ratelimit-requests-remaining": "10",
		"anthropic-ratelimit-tokens-remaining":   "0",
	}, `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`)
	assert.Equal(t, KindAnthropicRateLimit, ClassifyFailureKind("anthropic", anthropicRateLimit, nil))

	anthropicAPIError := respWithBody(500, nil, `{"type":"error","error":{"type":"api_error","message":"Internal error"}}`)
	assert.Equal(t, KindAnthropicAPIError, ClassifyFailureKind("anthropic", anthropicAPIError, nil))

	anthropicTimeout := respWithBody(504, nil, `{"type":"error","error":{"type":"timeout_error","message":"Request timed out"}}`)
	assert.Equal(t, KindAnthropicTimeout, ClassifyFailureKind("anthropic", anthropicTimeout, nil))

	openAIQuota := respWithBody(429, nil, `{"error":{"message":"You exceeded your current quota","type":"insufficient_quota","code":"insufficient_quota"}}`)
	assert.Equal(t, KindOpenAIInsufficientQuota, ClassifyFailureKind("openai", openAIQuota, nil))
}

func TestClassifyFailureKind_TransportErrors(t *testing.T) {
	cases := map[string]FailureKind{
		"connection reset by peer":    KindConnectionReset,
		"connection refused":          KindConnectionRefused,
		"tls handshake failure":       KindTLSHandshake,
		"no such host: example.com":   KindDNSFailure,
		"dial tcp: i/o timeout":       KindIOTimeout,
		"context deadline exceeded":   KindClientDeadline,
		"some random transport error": KindUnknownTransport,
	}
	for msg, want := range cases {
		got := ClassifyFailureKind("openai", nil, errors.New(msg))
		assert.Equal(t, want, got, "msg=%q", msg)
	}
	assert.Equal(t, KindClientCanceled, ClassifyFailureKind("openai", nil, context.Canceled))
	assert.Equal(t, KindClientDeadline, ClassifyFailureKind("openai", nil, context.DeadlineExceeded))
	assert.Equal(t, KindNilResponse, ClassifyFailureKind("openai", nil, nil))
}
