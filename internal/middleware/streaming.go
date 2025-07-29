package middleware

import (
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

// streamingResponseWriter wraps http.ResponseWriter to ensure proper flushing
type streamingResponseWriter struct {
	http.ResponseWriter
	flusher http.Flusher
}

func (sw *streamingResponseWriter) Write(b []byte) (int, error) {
	n, err := sw.ResponseWriter.Write(b)
	// Flush immediately for streaming responses
	sw.flusher.Flush()
	return n, err
}
