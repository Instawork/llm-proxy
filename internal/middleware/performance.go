package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/perf"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// ContextKey is a type for context keys to avoid collisions
type ContextKey string

const (
	// PerfTimingsKey is the context key for performance timings
	PerfTimingsKey ContextKey = "perf_timings"
)

// isProviderRoute checks if the request is for a provider route
func isProviderRoute(path string) bool {
	return strings.HasPrefix(path, "/openai/") ||
		strings.HasPrefix(path, "/anthropic/") ||
		strings.HasPrefix(path, "/gemini/")
}

// isAPIEndpoint checks if the request is for an API endpoint that should be performance tracked
func isAPIEndpoint(path string) bool {
	return strings.Contains(path, "/chat/completions") ||
		strings.Contains(path, "/completions") ||
		strings.Contains(path, "/messages") ||
		strings.Contains(path, ":generateContent")
}

// PerfTimings holds all the timing information for a request
type PerfTimings struct {
	RequestStart         time.Time // When the request first arrived at our proxy
	ProxyConnectionStart time.Time // When we started connecting to the proxied service
	ProxyConnectionEnd   time.Time // When the connection to the proxied service was established
	ProxyResponseStart   time.Time // When we started receiving the response from the proxied service
	ProxyResponseEnd     time.Time // When we finished receiving the response from the proxied service
	ResponseStart        time.Time // When we started sending the response back to the client
	ResponseEnd          time.Time // When we finished sending the response back to the client

	// Additional metrics
	RequestSize  int64 // Size of the request body in bytes
	ResponseSize int64 // Size of the response body in bytes
}

// CalculateMetrics calculates the performance metrics from the timing data
func (pt *PerfTimings) CalculateMetrics() perf.PerfTimings {
	var clientToProxyMs, proxyConnectionMs, proxyResponseMs, proxyToClientMs, totalRequestMs int64

	// Client to proxy: from request start to when we start connecting
	if !pt.RequestStart.IsZero() && !pt.ProxyConnectionStart.IsZero() {
		clientToProxyMs = pt.ProxyConnectionStart.Sub(pt.RequestStart).Milliseconds()
	}

	// Proxy connection: from start connecting to connection established
	if !pt.ProxyConnectionStart.IsZero() && !pt.ProxyConnectionEnd.IsZero() {
		proxyConnectionMs = pt.ProxyConnectionEnd.Sub(pt.ProxyConnectionStart).Milliseconds()
	}

	// Proxy response: from connection established to response fully received
	if !pt.ProxyConnectionEnd.IsZero() && !pt.ProxyResponseEnd.IsZero() {
		proxyResponseMs = pt.ProxyResponseEnd.Sub(pt.ProxyConnectionEnd).Milliseconds()
	}

	// Proxy to client: from response received to response sent to client
	if !pt.ProxyResponseEnd.IsZero() && !pt.ResponseEnd.IsZero() {
		proxyToClientMs = pt.ResponseEnd.Sub(pt.ProxyResponseEnd).Milliseconds()
	}

	// Total request: from start to end
	if !pt.RequestStart.IsZero() && !pt.ResponseEnd.IsZero() {
		totalRequestMs = pt.ResponseEnd.Sub(pt.RequestStart).Milliseconds()
	}

	return perf.PerfTimings{
		ClientToProxyMs:   clientToProxyMs,
		ProxyConnectionMs: proxyConnectionMs,
		ProxyResponseMs:   proxyResponseMs,
		ProxyToClientMs:   proxyToClientMs,
		TotalRequestMs:    totalRequestMs,
	}
}

// PerformanceTrackingMiddleware creates a middleware that tracks performance metrics
func PerformanceTrackingMiddleware(perfTracker *perf.PerfTracker, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip performance tracking for non-API routes
			if !isAPIEndpoint(r.URL.Path) || !isProviderRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Initialize performance timings
			timings := &PerfTimings{
				RequestStart: time.Now(),
			}

			// Add timings to request context
			ctx := context.WithValue(r.Context(), PerfTimingsKey, timings)
			r = r.WithContext(ctx)

			// Create a custom ResponseWriter to capture response information
			rw := &responseWriter{
				ResponseWriter: w,
				timings:        timings,
				logger:         logger,
			}

			// Process the request
			next.ServeHTTP(rw, r)

			// Mark the end of response
			timings.ResponseEnd = time.Now()

			// Track performance if we have LLM metadata in the context
			// Check the final request context in case it was updated by the handler
			if metadata, ok := providers.GetLLMResponseMetadataFromContext(r.Context()); ok {
				// Calculate performance metrics
				perfMetrics := timings.CalculateMetrics()

				// Extract user info from context or request
				userID := getUserIDFromRequest(r)
				ipAddress := getClientIP(r)

				// Track the performance
				err := perfTracker.TrackRequest(
					metadata,
					userID,
					ipAddress,
					r.URL.Path,
					perfMetrics,
					timings.RequestSize,
					timings.ResponseSize,
					rw.statusCode,
				)
				if err != nil {
					logger.Warn("Failed to track performance metrics", "error", err, "request_id", metadata.RequestID)
				} else {
					logger.Debug("Performance metrics tracked successfully",
						"request_id", metadata.RequestID,
						"total_ms", perfMetrics.TotalRequestMs,
						"provider", metadata.Provider,
						"model", metadata.Model)
				}
			}
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture response information
type responseWriter struct {
	http.ResponseWriter
	timings      *PerfTimings
	logger       *slog.Logger
	statusCode   int
	bytesWritten int64
	headersSent  bool
}

func (rw *responseWriter) WriteHeader(statusCode int) {
	if !rw.headersSent {
		rw.statusCode = statusCode
		rw.timings.ResponseStart = time.Now()
		rw.headersSent = true
		rw.ResponseWriter.WriteHeader(statusCode)
	}
}

func (rw *responseWriter) Write(data []byte) (int, error) {
	if !rw.headersSent {
		rw.WriteHeader(http.StatusOK)
	}

	n, err := rw.ResponseWriter.Write(data)
	rw.bytesWritten += int64(n)
	rw.timings.ResponseSize = rw.bytesWritten
	return n, err
}

// GetPerfTimingsFromContext retrieves performance timings from the request context
func GetPerfTimingsFromContext(ctx context.Context) (*PerfTimings, bool) {
	timings, ok := ctx.Value(PerfTimingsKey).(*PerfTimings)
	return timings, ok
}

// MarkProxyConnectionStart marks when we start connecting to the proxied service
func MarkProxyConnectionStart(ctx context.Context) {
	if timings, ok := GetPerfTimingsFromContext(ctx); ok {
		timings.ProxyConnectionStart = time.Now()
	}
}

// MarkProxyConnectionEnd marks when the connection to the proxied service is established
func MarkProxyConnectionEnd(ctx context.Context) {
	if timings, ok := GetPerfTimingsFromContext(ctx); ok {
		timings.ProxyConnectionEnd = time.Now()
	}
}

// MarkProxyResponseStart marks when we start receiving the response from the proxied service
func MarkProxyResponseStart(ctx context.Context) {
	if timings, ok := GetPerfTimingsFromContext(ctx); ok {
		timings.ProxyResponseStart = time.Now()
	}
}

// MarkProxyResponseEnd marks when we finish receiving the response from the proxied service
func MarkProxyResponseEnd(ctx context.Context) {
	if timings, ok := GetPerfTimingsFromContext(ctx); ok {
		timings.ProxyResponseEnd = time.Now()
	}
}

// SetRequestSize sets the size of the request body
func SetRequestSize(ctx context.Context, size int64) {
	if timings, ok := GetPerfTimingsFromContext(ctx); ok {
		timings.RequestSize = size
	}
}

// getUserIDFromRequest extracts user ID from the request (customize based on your auth system)
func getUserIDFromRequest(r *http.Request) string {
	// Try to get from Authorization header, query params, or other sources
	// This is a placeholder - implement based on your authentication system
	if userID := r.Header.Get("X-User-ID"); userID != "" {
		return userID
	}
	if userID := r.URL.Query().Get("user_id"); userID != "" {
		return userID
	}
	return "anonymous"
}

// getClientIP extracts the client IP address from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if idx := strings.Index(xff, ","); idx > 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	return r.RemoteAddr
}
