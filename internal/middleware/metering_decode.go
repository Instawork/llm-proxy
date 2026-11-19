package middleware

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Instawork/llm-proxy/internal/observability"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// maxDecodedBodyBytes bounds the decoded metering copy of a compressed
// response, so a small compressed capture cannot expand into an unbounded
// allocation. Four times maxCapturedBodyBytes leaves room for ordinary JSON
// and SSE compression ratios.
const maxDecodedBodyBytes = 4 * maxCapturedBodyBytes

// unparseableResponseMetric counts provider responses whose token usage could
// not be read (namespaced, e.g. llm.metering.unparseable_response).
const unparseableResponseMetric = "metering.unparseable_response"

// Reasons tagged on unparseableResponseMetric.
const (
	reasonUnsupportedEncoding = "unsupported_encoding"
	reasonDecodedTooLarge     = "decoded_too_large"
	reasonDecodeError         = "decode_error"
	reasonParseError          = "parse_error"
)

// decodeCapturedBody returns the captured response decoded per its
// Content-Encoding, or a non-empty reason when it cannot be. A streaming body
// that fails mid-stream (typically cut by the capture cap) is returned
// partially decoded so the provider's truncated-stream fallback can still
// recover usage from the events that arrived.
func decodeCapturedBody(captured []byte, contentEncoding string, isStreaming bool) ([]byte, string, error) {
	body, err := providers.DecodeContentEncoding(captured, contentEncoding, maxDecodedBodyBytes)
	switch {
	case err == nil:
		return body, "", nil
	case errors.Is(err, providers.ErrUnsupportedContentEncoding):
		return nil, reasonUnsupportedEncoding, err
	case errors.Is(err, providers.ErrDecodedBodyTooLarge):
		return nil, reasonDecodedTooLarge, err
	case isStreaming && len(body) > 0:
		return body, "", nil
	default:
		return nil, reasonDecodeError, err
	}
}

type unparseableResponse struct {
	provider        string
	reason          string
	contentEncoding string
	streaming       bool
	status          int
	capturedBytes   int
	err             error
}

// reportUnparseableResponse emits one counter and one warn line for a response
// that will go unmetered. Neither carries body bytes, keys, or user data.
func reportUnparseableResponse(metrics observability.MetricsSink, u unparseableResponse) {
	encoding := encodingTagValue(u.contentEncoding)
	statusClass := strconv.Itoa(u.status/100) + "xx"
	if metrics != nil {
		_ = metrics.Incr(unparseableResponseMetric, []string{
			"provider:" + normalizeDogstatsdTag(u.provider),
			"reason:" + u.reason,
			"encoding:" + encoding,
			"streaming:" + strconv.FormatBool(u.streaming),
			"status_class:" + statusClass,
		}, 1.0)
	}
	errText := ""
	if u.err != nil {
		errText = u.err.Error()
	}
	proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "response usage unparseable; request unmetered",
		slog.String("provider", u.provider),
		slog.String("reason", u.reason),
		slog.String("encoding", encoding),
		slog.Bool("streaming", u.streaming),
		slog.Int("status", u.status),
		slog.Int("captured_bytes", u.capturedBytes),
		slog.String("error", errText),
	)
}

// isEncodedContent reports whether a Content-Encoding value means the body
// bytes are not plain text.
func isEncodedContent(contentEncoding string) bool {
	for part := range strings.SplitSeq(contentEncoding, ",") {
		if coding := strings.ToLower(strings.TrimSpace(part)); coding != "" && coding != "identity" {
			return true
		}
	}
	return false
}

// encodingLogNote is the short coding label on the streaming 📊 summary line.
func encodingLogNote(contentEncoding string) string {
	if v := encodingTagValue(contentEncoding); v != "gzip" {
		return v
	}
	return "gz"
}

// maxEncodingTagLen caps the encoding tag; the value comes from an upstream
// header, so it is also restricted to a safe character set.
const maxEncodingTagLen = 32

// encodingTagValue normalizes a Content-Encoding value for a DogStatsD tag:
// "identity" when absent, stacked codings joined with "+", "other" for
// anything unexpected.
func encodingTagValue(contentEncoding string) string {
	codings := make([]string, 0, 2)
	for part := range strings.SplitSeq(contentEncoding, ",") {
		if coding := strings.ToLower(strings.TrimSpace(part)); coding != "" {
			codings = append(codings, coding)
		}
	}
	if len(codings) == 0 {
		return "identity"
	}
	v := strings.Join(codings, "+")
	if len(v) > maxEncodingTagLen {
		return "other"
	}
	for _, c := range v {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '+' && c != '_' {
			return "other"
		}
	}
	return v
}
