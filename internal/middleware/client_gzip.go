package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

var errClientGzipWriteAfterFinish = errors.New("client gzip: write after finish")

type clientAcceptEncodingCtxKey struct{}

// ClientGzipMiddleware re-compresses non-streaming provider responses for
// clients that sent Accept-Encoding: gzip. Register only when enabled in config.
func ClientGzipMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientAE := r.Header.Get("Accept-Encoding")
			ctx := context.WithValue(r.Context(), clientAcceptEncodingCtxKey{}, clientAE)
			r = r.WithContext(ctx)

			if providerManager.IsStreamingRequest(r) || !clientAcceptsGzip(clientAE) {
				next.ServeHTTP(w, r)
				return
			}

			gw := &clientGzipResponseWriter{ResponseWriter: w}
			next.ServeHTTP(gw, r)
			if err := gw.finish(); err != nil {
				proxylog.SlogProxy(slog.Default(), slog.LevelError, "client_gzip: failed to compress response",
					slog.String("error", err.Error()),
					slog.String("path", r.URL.Path))
				if !gw.headerSent {
					proxylog.ProxyHTTPError(w, "response compression failed", http.StatusInternalServerError)
				}
			}
		})
	}
}

func clientAcceptEncodingFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(clientAcceptEncodingCtxKey{}).(string)
	return v
}

func clientAcceptsGzip(acceptEncoding string) bool {
	if acceptEncoding == "" {
		return false
	}
	for _, part := range strings.Split(acceptEncoding, ",") {
		enc := strings.TrimSpace(strings.Split(part, ";")[0])
		if enc == "gzip" || enc == "*" {
			return true
		}
	}
	return false
}

type clientGzipResponseWriter struct {
	http.ResponseWriter
	buf        bytes.Buffer
	statusCode int
	headerSent bool
	finished   bool
}

func (cw *clientGzipResponseWriter) WriteHeader(statusCode int) {
	if cw.headerSent || cw.finished {
		return
	}
	cw.statusCode = statusCode
}

func (cw *clientGzipResponseWriter) Write(b []byte) (int, error) {
	if cw.finished {
		return 0, errClientGzipWriteAfterFinish
	}
	if len(b) == 0 {
		return 0, nil
	}
	return cw.buf.Write(b)
}

func (cw *clientGzipResponseWriter) finish() error {
	if cw.finished {
		return nil
	}
	cw.finished = true

	if cw.statusCode == 0 {
		cw.statusCode = http.StatusOK
	}

	if cw.buf.Len() == 0 {
		cw.headerSent = true
		cw.ResponseWriter.WriteHeader(cw.statusCode)
		return nil
	}

	var compressed bytes.Buffer
	gw := gzip.NewWriter(&compressed)
	if _, err := gw.Write(cw.buf.Bytes()); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}

	h := cw.ResponseWriter.Header()
	h.Del("Content-Length")
	h.Del("Content-Encoding")
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(compressed.Len()))
	h.Add("Vary", "Accept-Encoding")

	cw.headerSent = true
	cw.ResponseWriter.WriteHeader(cw.statusCode)
	_, err := cw.ResponseWriter.Write(compressed.Bytes())
	return err
}
