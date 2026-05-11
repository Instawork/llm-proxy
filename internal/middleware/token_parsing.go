package middleware

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// MetadataCallback is a function that can be hooked into the TokenParsingMiddleware
// to process LLM response metadata.
type MetadataCallback func(r *http.Request, metadata *providers.LLMResponseMetadata)

// GetProviderFromRequest determines which provider to use based on the request path
func GetProviderFromRequest(providerManager *providers.ProviderManager, req *http.Request) providers.Provider {
	path := req.URL.Path

	// Handle /meta/:userID/provider/ pattern first
	if strings.HasPrefix(path, "/meta/") {
		parts := strings.Split(path, "/")
		if len(parts) >= 4 { // ["", "meta", "userID", "provider", ...]
			providerName := parts[3]
			switch providerName {
			case "openai":
				return providerManager.GetProvider("openai")
			case "anthropic":
				return providerManager.GetProvider("anthropic")
			case "gemini":
				return providerManager.GetProvider("gemini")
			}
		}
	}

	// Handle direct provider paths (legacy support)
	if strings.HasPrefix(path, "/openai/") {
		return providerManager.GetProvider("openai")
	} else if strings.HasPrefix(path, "/anthropic/") {
		return providerManager.GetProvider("anthropic")
	} else if strings.HasPrefix(path, "/gemini/") {
		return providerManager.GetProvider("gemini")
	}

	return nil
}

// TokenParsingMiddleware intercepts responses to parse and log token usage.
// It also measures time-to-first-byte (TTFB) — the elapsed milliseconds from
// when the proxy handler is entered to when the first byte is written back to
// the caller.  The measurement is attached to the parsed LLMResponseMetadata so
// that registered callbacks (e.g. the cost tracker) can record it.
func TokenParsingMiddleware(providerManager *providers.ProviderManager, callbacks ...MetadataCallback) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provider := GetProviderFromRequest(providerManager, r)
			isStreaming := providerManager.IsStreamingRequest(r)
			requestStart := time.Now()

			captureWriter := &responseCapture{
				ResponseWriter: w,
				body:           &bytes.Buffer{},
				isStreaming:    isStreaming,
				provider:       provider,
				requestStart:   requestStart,
			}

			next.ServeHTTP(captureWriter, r)

			isAPIEndpoint := strings.Contains(r.URL.Path, "/chat/completions") ||
				strings.Contains(r.URL.Path, "/completions") ||
				strings.Contains(r.URL.Path, "/messages") ||
				strings.Contains(r.URL.Path, ":generateContent") ||
				strings.Contains(r.URL.Path, ":streamGenerateContent")

			if provider == nil || !isAPIEndpoint {
				return
			}

			totalElapsed := time.Since(requestStart)

			var metadata *providers.LLMResponseMetadata
			var err error

			if isStreaming && captureWriter.lastMetadata != nil {
				metadata = captureWriter.lastMetadata
			} else {
				bodyReader := bytes.NewReader(captureWriter.body.Bytes())
				metadata, err = provider.ParseResponseMetadata(bodyReader, isStreaming)
			}

			if err != nil {
				if !isStreaming {
					log.Printf("Warning: failed to parse response metadata for %s: %v", provider.GetName(), err)
					if captureWriter.body.Len() > 0 {
						bodyBytes := captureWriter.body.Bytes()
						if len(bodyBytes) >= 2 && bodyBytes[0] == 0x1f && bodyBytes[1] == 0x8b {
							if decompressed, e := decompressForPreview(bodyBytes); e == nil {
								previewLen := min(200, len(decompressed))
								log.Printf("🔍 Response body (gzip-decompressed): %s", string(decompressed[:previewLen]))
							}
						} else {
							log.Printf("🔍 Response body preview: %s", string(bodyBytes[:min(200, len(bodyBytes))]))
						}
					}
				}
				return
			}

			if metadata == nil {
				return
			}

			// Attach TTFB.
			if captureWriter.ttfbMS > 0 {
				metadata.TTFBMS = captureWriter.ttfbMS
			}

			// Emit a single, human-readable summary line.
			cacheNote := ""
			if metadata.CacheReadInputTokens > 0 || metadata.CacheCreationInputTokens > 0 {
				cacheNote = fmt.Sprintf(" | cache_read=%d cache_write=%d", metadata.CacheReadInputTokens, metadata.CacheCreationInputTokens)
			}
			thoughtNote := ""
			if metadata.ThoughtTokens > 0 {
				thoughtNote = fmt.Sprintf(" | thought=%d", metadata.ThoughtTokens)
			}
			log.Printf("📊 LLM %s/%s | in=%d out=%d total=%d%s%s | ttfb=%dms total=%dms | req=%s",
				metadata.Provider, metadata.Model,
				metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens,
				cacheNote, thoughtNote,
				metadata.TTFBMS, totalElapsed.Milliseconds(),
				metadata.RequestID)

			// Response headers (best-effort; headers may already be flushed for streaming).
			w.Header().Set("X-LLM-Input-Tokens", fmt.Sprintf("%d", metadata.InputTokens))
			w.Header().Set("X-LLM-Output-Tokens", fmt.Sprintf("%d", metadata.OutputTokens))
			w.Header().Set("X-LLM-Total-Tokens", fmt.Sprintf("%d", metadata.TotalTokens))
			w.Header().Set("X-LLM-Thought-Tokens", fmt.Sprintf("%d", metadata.ThoughtTokens))
			w.Header().Set("X-LLM-Cache-Read-Tokens", fmt.Sprintf("%d", metadata.CacheReadInputTokens))
			w.Header().Set("X-LLM-Cache-Write-Tokens", fmt.Sprintf("%d", metadata.CacheCreationInputTokens))
			w.Header().Set("X-LLM-TTFB-MS", fmt.Sprintf("%d", metadata.TTFBMS))
			w.Header().Set("X-LLM-Provider", metadata.Provider)
			w.Header().Set("X-LLM-Model", metadata.Model)
			if metadata.RequestID != "" {
				w.Header().Set("X-LLM-Request-ID", metadata.RequestID)
			}

			for _, callback := range callbacks {
				if callback != nil {
					callback(r, metadata)
				}
			}
		})
	}
}

// responseCapture captures the response body for parsing and measures TTFB.
type responseCapture struct {
	http.ResponseWriter

	body         *bytes.Buffer
	isStreaming  bool
	provider     providers.Provider
	lastMetadata *providers.LLMResponseMetadata
	requestStart time.Time

	// TTFB bookkeeping — written once on the first non-empty Write call.
	ttfbOnce sync.Once
	ttfbMS   int64

	// lastParsedPos tracks how many bytes we have already tried to parse so we
	// avoid re-scanning the same prefix on every chunk.
	lastParsedPos int
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	if len(b) > 0 {
		rc.ttfbOnce.Do(func() {
			rc.ttfbMS = time.Since(rc.requestStart).Milliseconds()
			log.Printf("⏱  TTFB: %dms", rc.ttfbMS)
		})
	}

	rc.body.Write(b)

	// For streaming responses incrementally try to parse usage info.  The usage
	// summary only appears in the final message_stop event, so most attempts will
	// silently fail — that's expected and we no longer log those non-errors.
	if rc.isStreaming && rc.provider != nil {
		allData := rc.body.Bytes()
		if len(allData) > rc.lastParsedPos {
			bodyReader := bytes.NewReader(allData)
			if metadata, err := rc.provider.ParseResponseMetadata(bodyReader, true); err == nil && metadata != nil {
				rc.lastMetadata = metadata
			}
			rc.lastParsedPos = len(allData)
		}
	}

	return rc.ResponseWriter.Write(b)
}

// Flush implements http.Flusher so that downstream middleware (StreamingMiddleware)
// can detect Flusher capability on the wrapped writer. Without this, the type
// assertion `w.(http.Flusher)` fails when responseCapture is in the chain,
// causing SSE responses from LLM providers to be buffered until the request
// completes — the entire streamed response is delivered in a single chunk.
//
// Delegates to the underlying ResponseWriter's Flush when supported; otherwise
// it is a safe no-op. This makes streaming work for all providers (OpenAI,
// Anthropic, Gemini) that emit SSE through the proxy.
func (rc *responseCapture) Flush() {
	if flusher, ok := rc.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker by delegating to the underlying ResponseWriter
// when it supports hijacking. LLM endpoints use SSE rather than HTTP upgrades,
// so this is mostly defensive: it preserves transparency in case any consumer
// (e.g. a future websocket-style endpoint proxied through here) needs Hijack.
// Returns http.ErrNotSupported when the underlying writer does not support it.
func (rc *responseCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := rc.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.ErrUnsupported
}

// Helper function to find minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ExtractUserIDFromRequest extracts user ID from request headers, query parameters, or provider-specific methods
// Follows the priority order: context (from meta URL) → URL path → headers → query parameters → provider-specific extraction → fallback to IP
func ExtractUserIDFromRequest(req *http.Request, provider providers.Provider) string {
	// Priority 0: Check for user ID in request context (from meta URL rewriting)
	if userID, ok := req.Context().Value(userIDContextKey).(string); ok && userID != "" {
		log.Printf("🔍 User ID from context: %s", userID)
		return userID
	}

	// Priority 1: Check for user ID in URL path for meta prefix pattern
	path := req.URL.Path
	if strings.HasPrefix(path, "/meta/") {
		parts := strings.Split(path, "/")
		if len(parts) >= 3 { // ["", "meta", "userID", ...]
			userID := parts[2]
			if userID != "" {
				log.Printf("🔍 User ID from URL path: %s", userID)
				return userID
			}
		}
	}

	// Priority 2: Check for custom user ID header
	if userID := req.Header.Get("X-User-ID"); userID != "" {
		log.Printf("🔍 User ID from X-User-ID header: %s", userID)
		return userID
	}

	// Priority 3: Provider-specific extraction from request body
	if provider != nil {
		if userID := provider.UserIDFromRequest(req); userID != "" {
			log.Printf("🔍 User ID from provider-specific extraction: %s", userID)
			return userID
		}
	}

	// Priority 4: Check query parameters
	if userID := req.URL.Query().Get("llm_user_id"); userID != "" {
		log.Printf("🔍 User ID from query parameter: %s", userID)
		return userID
	}

	// Priority 5: Check Authorization header for API key or JWT token
	if auth := req.Header.Get("Authorization"); auth != "" {
		// For API keys, use a hash of the key as user ID (for privacy)
		if strings.HasPrefix(auth, "Bearer ") {
			// Use first 8 characters of the token for identification
			token := auth[7:]
			if len(token) > 8 {
				tokenID := fmt.Sprintf("token:%s", token[:8])
				log.Printf("🔍 User ID from Authorization header: %s", tokenID)
				return tokenID
			}
			tokenID := fmt.Sprintf("token:%s", token)
			log.Printf("🔍 User ID from Authorization header: %s", tokenID)
			return tokenID
		}
	}

	// Fallback to IP address if no user identification
	ipAddr := ExtractIPAddressFromRequest(req)
	log.Printf("🔍 User ID fallback to IP address: %s", ipAddr)
	return fmt.Sprintf("ip:%s", ipAddr)
}

// ExtractIPAddressFromRequest extracts IP address from request headers
func ExtractIPAddressFromRequest(req *http.Request) string {
	// Check for forwarded headers
	if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
		return forwarded
	}

	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}

	return req.RemoteAddr
}

// decompressForPreview safely decompresses gzip data for debug preview purposes
func decompressForPreview(data []byte) ([]byte, error) {
	// Check for gzip magic number
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return nil, fmt.Errorf("not gzip compressed")
	}

	// Create a gzip reader
	reader := bytes.NewReader(data)
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	// Read the decompressed data
	decompressed, err := io.ReadAll(gzipReader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress data: %w", err)
	}

	return decompressed, nil
}
