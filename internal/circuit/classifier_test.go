package circuit

import (
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func resp(status int, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: http.NoBody}
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
