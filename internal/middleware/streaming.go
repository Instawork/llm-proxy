package middleware

import (
	"context"
	"log"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// StreamingMiddleware ensures proper handling of streaming responses
func StreamingMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if this is likely a streaming request using the provider manager
			if providerManager.IsStreamingRequest(r) {
				// Ensure we can flush the response
				if flusher, ok := w.(http.Flusher); ok {
					// Wrap the ResponseWriter to ensure proper flushing for streaming
					streamingWriter := &streamingResponseWriter{
						ResponseWriter: w,
						flusher:        flusher,
						ctx:            r.Context(),
					}
					next.ServeHTTP(streamingWriter, r)
				} else {
					log.Printf("Warning: ResponseWriter does not support flushing for streaming request")
					next.ServeHTTP(w, r)
				}
			} else {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// streamingResponseWriter wraps http.ResponseWriter to ensure proper flushing.
//
// It also short-circuits writes once the request context is canceled — for
// example when the upstream client closes its SSE connection mid-stream.
// Without this guard the reverse proxy keeps copying provider-emitted
// chunks into a dead socket, wasting upstream tokens and (under high
// concurrency) tying up worker goroutines waiting on the broken Write.
type streamingResponseWriter struct {
	http.ResponseWriter
	flusher http.Flusher
	ctx     context.Context
}

func (sw *streamingResponseWriter) Write(b []byte) (int, error) {
	// ctx is optional so tests that hand-construct this struct (and the
	// fast path inside the middleware which provides one) both work.
	if sw.ctx != nil {
		if err := sw.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := sw.ResponseWriter.Write(b)
	// Flush immediately for streaming responses
	sw.flusher.Flush()
	return n, err
}
