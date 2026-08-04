package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/redact"
)

type piiRegistryCtxKey struct{}

// PIIRegistry returns the per-request placeholder registry when wire-mode
// scrubbing ran successfully.
func PIIRegistry(ctx interface{ Value(any) any }) (*redact.Registry, bool) {
	v, ok := ctx.Value(piiRegistryCtxKey{}).(*redact.Registry)
	return v, ok && v != nil && v.Len() > 0
}

// PIIResponseRestoreMiddleware restores MASK-tier placeholders in upstream
// responses before they reach the client. SEAL placeholders and REDACT
// markers pass through unchanged.
//
// Why we do not use HTTP trailers for X-LLM-PII-Restored / X-LLM-PII-Leaked:
// this service is commonly reached through Cloudflare (orange-cloud DNS).
// Cloudflare's edge does not reliably proxy response trailers. With
// Accept-Encoding: gzip (httpx default) a Trailer-bearing response becomes a
// Cloudflare-branded 502 HTML page even when origin returned 200 JSON;
// without gzip, clients often see incomplete chunked reads or HTTP/2
// INTERNAL_ERROR after the body. So Restored/Leaked must not be announced
// via Trailer.
//
// Non-streaming: buffer the restored body, then emit Restored/Leaked as
// normal response headers before writing bytes.
// Streaming: Restored/Leaked are only known after the body ends, and we
// cannot use trailers — flush early PII headers (Detected/Masked/…) and omit
// Restored/Leaked on the wire (still finalized for logs). Chunks still flush
// through immediately.
func PIIResponseRestoreMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg, ok := PIIRegistry(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			isStreaming := providerManager.IsStreamingRequest(r)
			if isStreaming {
				servePIIStreamingRestore(w, r, next, reg)
				return
			}
			servePIIBufferedRestore(w, r, next, reg)
		})
	}
}

func servePIIStreamingRestore(w http.ResponseWriter, r *http.Request, next http.Handler, reg *redact.Registry) {
	headerWriter := &piiStreamHeaderResponseWriter{
		ResponseWriter: w,
		ctx:            r.Context(),
	}
	restoreWriter := &piiRestoreResponseWriter{
		ResponseWriter: headerWriter,
		registry:       reg,
		streaming:      true,
	}
	next.ServeHTTP(restoreWriter, r)

	if tail := restoreWriter.flushCarryTail(); len(tail) > 0 {
		_, _ = restoreWriter.Write(tail)
	}
	finalizePIIRestored(r.Context(), reg)
	finalizePIILeaked(r.Context(), reg, restoreWriter.emitted.String())
	logPIILeakIfNeeded(r, true)
}

func servePIIBufferedRestore(w http.ResponseWriter, r *http.Request, next http.Handler, reg *redact.Registry) {
	bufWriter := &piiBufferResponseWriter{
		ResponseWriter: w,
		ctx:            r.Context(),
	}
	restoreWriter := &piiRestoreResponseWriter{
		ResponseWriter: bufWriter,
		registry:       reg,
		streaming:      false,
	}
	next.ServeHTTP(restoreWriter, r)

	finalizePIIRestored(r.Context(), reg)
	finalizePIILeaked(r.Context(), reg, restoreWriter.emitted.String())
	if err := bufWriter.commit(); err != nil {
		proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: failed to commit buffered response",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path))
	}
	logPIILeakIfNeeded(r, false)
}

func logPIILeakIfNeeded(r *http.Request, streaming bool) {
	h := piiSummaryHolderFromContext(r.Context())
	if h == nil || h.Leaked <= 0 {
		return
	}
	proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: MASK placeholders leaked in response",
		slog.Int("leaked", h.Leaked),
		slog.Int("restored", h.Restored),
		slog.Bool("streaming", streaming),
		slog.String("path", r.URL.Path))
}

type piiRestoreResponseWriter struct {
	http.ResponseWriter
	registry    *redact.Registry
	streaming   bool
	carry       []byte
	emitted     bytes.Buffer
	jsonMode    bool
	jsonModeSet bool
}

// useJSONRestore decides, once per response, whether restored originals
// need JSON-string escaping. Every provider response this proxy handles is
// JSON (or JSON-per-SSE-line); the Content-Type sniff below only exists so
// a genuinely plain-text response — none exist among current providers,
// but nothing guarantees that stays true — falls back to the verbatim
// restore instead of corrupting non-JSON bytes with stray backslashes.
//
// Content-Type is safe to read on the first Write: httputil.ReverseProxy
// copies the upstream response headers onto pw.Header() before it starts
// copying the body, for both buffered and streamed responses.
func (pw *piiRestoreResponseWriter) useJSONRestore() bool {
	if !pw.jsonModeSet {
		pw.jsonMode = responseLooksLikeJSON(pw.Header().Get("Content-Type"))
		pw.jsonModeSet = true
	}
	return pw.jsonMode
}

func (pw *piiRestoreResponseWriter) flushCarryTail() []byte {
	if pw.useJSONRestore() {
		return pw.registry.FlushCarryJSON(pw.carry)
	}
	return pw.registry.FlushCarry(pw.carry)
}

// responseLooksLikeJSON reports whether a response Content-Type carries a
// JSON (or JSON-per-SSE-line) body. An empty Content-Type defaults to true
// since every provider this proxy fronts responds with JSON by default.
func responseLooksLikeJSON(contentType string) bool {
	ct := strings.ToLower(contentType)
	if ct == "" {
		return true
	}
	return strings.Contains(ct, "json") || strings.Contains(ct, "event-stream")
}

func (pw *piiRestoreResponseWriter) Flush() {
	if f, ok := pw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (pw *piiRestoreResponseWriter) writeRestored(restored []byte) (int, error) {
	if len(restored) == 0 {
		return 0, nil
	}
	pw.emitted.Write(restored)
	n, err := pw.ResponseWriter.Write(restored)
	if err != nil {
		return n, err
	}
	return len(restored), nil
}

func (pw *piiRestoreResponseWriter) Write(b []byte) (int, error) {
	if pw.registry == nil || len(b) == 0 {
		return pw.ResponseWriter.Write(b)
	}
	// On success every branch must report exactly len(b) consumed, even when
	// restoration (or gzip decompression) changes the byte count.
	// httputil.ReverseProxy copies the upstream body with io.Copy, which
	// treats n < len(b) as io.ErrShortWrite and n > len(b) as an invalid
	// write — either aborts the copy and panics with http.ErrAbortHandler,
	// surfacing to clients as an ALB 502 with no response body.
	if !pw.streaming {
		plain, _, err := decompressPIIResponseIfGzip(b)
		if err != nil {
			proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: gzip decompress failed; passing through without placeholder restore",
				slog.String("error", err.Error()))
			if _, err := pw.writeRestored(b); err != nil {
				return 0, err
			}
			return len(b), nil
		}
		var restored string
		if pw.useJSONRestore() {
			restored = pw.registry.RestoreUserFacingJSON(string(plain))
		} else {
			restored = pw.registry.RestoreUserFacing(string(plain))
		}
		if _, err := pw.writeRestored([]byte(restored)); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	if pw.streaming {
		var emit, newCarry []byte
		if pw.useJSONRestore() {
			emit, newCarry = pw.registry.RestoreStreamChunkJSON(b, pw.carry)
		} else {
			emit, newCarry = pw.registry.RestoreStreamChunk(b, pw.carry)
		}
		pw.carry = newCarry
		if len(emit) == 0 {
			return len(b), nil
		}
		if _, err := pw.writeRestored(emit); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return len(b), nil
}

// forceStreamingOff rewrites a JSON request body to set "stream": false.
// Best-effort: returns the original body when parsing fails.
func forceStreamingOff(body []byte) []byte {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	if _, ok := root["stream"]; !ok {
		return body
	}
	root["stream"] = false
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}
