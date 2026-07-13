package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

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
func PIIResponseRestoreMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reg, ok := PIIRegistry(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			isStreaming := providerManager.IsStreamingRequest(r)
			deferWriter := &piiDeferHeadersResponseWriter{
				ResponseWriter: w,
				ctx:            r.Context(),
			}
			restoreWriter := &piiRestoreResponseWriter{
				ResponseWriter: deferWriter,
				registry:       reg,
				streaming:      isStreaming,
			}
			next.ServeHTTP(restoreWriter, r)

			if isStreaming {
				if tail := reg.FlushCarry(restoreWriter.carry); len(tail) > 0 {
					_, _ = restoreWriter.Write(tail)
				}
			}
			finalizePIIRestored(r.Context(), reg)
			finalizePIILeaked(r.Context(), reg, restoreWriter.emitted.String())
			if err := writePIIResponseTrailers(deferWriter, r.Context()); err != nil {
				proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: failed to set PII trailers",
					slog.String("error", err.Error()),
					slog.String("path", r.URL.Path))
			}
			if h := piiSummaryHolderFromContext(r.Context()); h != nil && h.Leaked > 0 {
				proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: MASK placeholders leaked in response",
					slog.Int("leaked", h.Leaked),
					slog.Int("restored", h.Restored),
					slog.Bool("streaming", isStreaming),
					slog.String("path", r.URL.Path))
			}
		})
	}
}

type piiRestoreResponseWriter struct {
	http.ResponseWriter
	registry  *redact.Registry
	streaming bool
	carry     []byte
	emitted   bytes.Buffer
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
	if !pw.streaming {
		plain, _, err := decompressPIIResponseIfGzip(b)
		if err != nil {
			proxylog.SlogProxy(slog.Default(), slog.LevelWarn, "pii_restore: gzip decompress failed; passing through without placeholder restore",
				slog.String("error", err.Error()))
			return pw.writeRestored(b)
		}
		restored := pw.registry.RestoreUserFacing(string(plain))
		return pw.writeRestored([]byte(restored))
	}
	if pw.streaming {
		emit, newCarry := pw.registry.RestoreStreamChunk(b, pw.carry)
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
