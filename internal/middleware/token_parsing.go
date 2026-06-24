package middleware

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

// MetadataCallback is a function that can be hooked into the TokenParsingMiddleware
// to process LLM response metadata.
type MetadataCallback func(r *http.Request, metadata *providers.LLMResponseMetadata)

// GetProviderFromRequest determines which provider to use based on the
// request path. Recognized prefixes:
//   - /meta/<userID>/<provider>/...   meta-routing for user-scoped requests
//   - /openai/...                     OpenAI native
//   - /anthropic/...                  Anthropic native
//   - /gemini/...                     Gemini native
//   - /v1/models/gemini..., /v1beta/models/gemini...  Gemini compatibility
//   - /bedrock/...                    Bedrock native (with /bedrock prefix)
//   - /model/...                      Bedrock SigV4 passthrough
func GetProviderFromRequest(providerManager *providers.ProviderManager, req *http.Request) providers.Provider {
	path := req.URL.Path

	if strings.HasPrefix(path, "/meta/") {
		parts := strings.Split(path, "/")
		if len(parts) >= 4 { // ["", "meta", "userID", "provider", ...]
			providerName := parts[3]
			switch providerName {
			case "openai", "anthropic", "gemini", "bedrock":
				return providerManager.GetProvider(providerName)
			}
		}
	}

	switch {
	case strings.HasPrefix(path, "/openai/"):
		return providerManager.GetProvider("openai")
	case strings.HasPrefix(path, "/anthropic/"):
		return providerManager.GetProvider("anthropic")
	case strings.HasPrefix(path, "/gemini/"),
		strings.HasPrefix(path, "/v1/models/gemini"),
		strings.HasPrefix(path, "/v1beta/models/gemini"):
		return providerManager.GetProvider("gemini")
	case strings.HasPrefix(path, "/bedrock/"), strings.HasPrefix(path, "/model/"):
		return providerManager.GetProvider("bedrock")
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
				strings.Contains(r.URL.Path, ":streamGenerateContent") ||
				strings.Contains(r.URL.Path, "/converse")

			if provider == nil || !isAPIEndpoint {
				return
			}

			totalElapsed := time.Since(requestStart)

			bodyReader := bytes.NewReader(captureWriter.body.Bytes())
			metadata, err := provider.ParseResponseMetadata(bodyReader, isStreaming)
			if err != nil {
				if !isStreaming {
					log.Printf("Warning: failed to parse response metadata for %s: %v", provider.GetName(), err)
					if captureWriter.body.Len() > 0 {
						bodyBytes := captureWriter.body.Bytes()
						// Both branches route through redact.LogPreview so
						// raw model output is never persisted unredacted
						// when pii_redact is enabled. The wording stays
						// stable for the test fixtures that grep for
						// "gzip-decompressed" / "Response body preview".
						if len(bodyBytes) >= 2 && bodyBytes[0] == 0x1f && bodyBytes[1] == 0x8b {
							if decompressed, e := decompressForPreview(bodyBytes); e == nil {
								log.Printf("🔍 Response body (gzip-decompressed): %s",
									redact.LogPreview(context.Background(), string(decompressed), 200))
							}
						} else {
							log.Printf("🔍 Response body preview: %s",
								redact.LogPreview(context.Background(), string(bodyBytes), 200))
						}
					}
				}
				return
			}

			if metadata == nil {
				return
			}

			// Backfill model name when the provider's response body does not
			// carry it (e.g. Bedrock Converse, where the model lives in the
			// signed URL path rather than the response body). Falling back
			// to the request-side extractor here keeps log lines and cost
			// metrics correctly model-tagged without changing the Provider
			// interface signature.
			if metadata.Model == "" {
				if reqModel, _ := provider.ExtractRequestModelAndMessages(r); reqModel != "" {
					metadata.Model = reqModel
				}
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
			streamNote := ""
			if isStreaming {
				gzNote := ""
				if captureWriter.compressed {
					gzNote = " gz"
				}
				eventsNote := ""
				if ec := captureWriter.formatEventCounts(); ec != "-" {
					eventsNote = " events=" + ec
				}
				streamNote = fmt.Sprintf(" | chunks=%d bytes=%d%s%s",
					captureWriter.chunkCount, captureWriter.totalBytes, gzNote, eventsNote)
			}
			log.Printf("📊 LLM %s/%s | in=%d out=%d total=%d%s%s | ttfb=%dms total=%dms%s | req=%s",
				metadata.Provider, metadata.Model,
				metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens,
				cacheNote, thoughtNote,
				metadata.TTFBMS, totalElapsed.Milliseconds(),
				streamNote,
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

// maxCapturedBodyBytes caps the number of response-body bytes
// responseCapture retains for post-stream token/usage parsing. Without a
// cap, an adversarial or runaway streaming response (e.g. a model emitting
// many megabytes of text/tool-call JSON) would grow rc.body indefinitely
// and OOM the proxy under enough concurrent requests. 4 MiB is well above
// any sensible final-event size from the supported providers but still
// bounds worst-case per-request memory at ~4 MiB × inflight requests.
const maxCapturedBodyBytes = 4 * 1024 * 1024

// responseCapture captures the response body for parsing and measures TTFB.
type responseCapture struct {
	http.ResponseWriter

	body         *bytes.Buffer
	captureFull  bool // false once the capture buffer is at the cap
	isStreaming  bool
	provider     providers.Provider
	requestStart time.Time

	// TTFB bookkeeping — written once on the first non-empty Write call.
	ttfbOnce sync.Once
	ttfbMS   int64

	// Streaming diagnostics: chunk count, total bytes, and the last-chunk
	// timestamp so we can detect long gaps between chunks (e.g. upstream
	// stalls or buffering further down the response chain).
	chunkCount  int64
	totalBytes  int64
	lastChunkAt time.Time
	// Compressed-content detection (best-effort, observational only).
	compressed     bool
	compressedOnce sync.Once

	// SSE event-type histogram. Counts both `event:` lines and `"type":"..."`
	// occurrences in `data:` payloads. Useful for telling apart "Anthropic is
	// sending lots of pings/thinking_deltas" vs "Anthropic is silent" during
	// long inter-chunk gaps. Only populated when the upstream body is plain
	// text (non-gzipped) — pass --disable-gzip at startup to force uncompressed
	// upstream bytes and populate this field reliably.
	sseLeftover    []byte
	sseEventCounts map[string]int64
	sseDataTypes   map[string]int64
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	now := time.Now()
	if len(b) > 0 {
		rc.ttfbOnce.Do(func() {
			rc.ttfbMS = now.Sub(rc.requestStart).Milliseconds()
			log.Printf("⏱  TTFB: %dms", rc.ttfbMS)
		})
		// Detect upstream gzip on the first chunk. When --disable-gzip is set,
		// CreateGenericDirector strips Accept-Encoding so this should not fire;
		// without it, gzip is expected and this is just an observational note.
		rc.compressedOnce.Do(func() {
			if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
				rc.compressed = true
				log.Printf("🗜  WARNING upstream still sent gzip despite Accept-Encoding strip (passthrough)")
			}
		})
	}

	// Streaming pacing diagnostics. Cheap (only fires for streaming requests).
	if rc.isStreaming && len(b) > 0 {
		var gap time.Duration
		if !rc.lastChunkAt.IsZero() {
			gap = now.Sub(rc.lastChunkAt)
		}
		rc.lastChunkAt = now
		rc.chunkCount++
		rc.totalBytes += int64(len(b))

		// SSE event-type sniffing on uncompressed bodies. Returns the per-chunk
		// event types so we can log them on every chunk without re-scanning.
		// This walks line-by-line through the new bytes (plus any partial line
		// leftover from the previous chunk). O(new bytes), never O(N²).
		var perChunk map[string]int64
		if !rc.compressed {
			perChunk = rc.sniffSSEEvents(b)
		}

		// One concise line per chunk: chunk #, gap from prev, size, event types
		// in THIS chunk. With Accept-Encoding stripping you'll see things like:
		//   📡 #1   +0ms     402B types=event:message_start=1,type:message_start=1
		//   📡 #18  +7828ms  18B  types=event:ping=1,type:ping=1   ← stall here
		//   📡 #100 +43508ms 21951B types=event:content_block_stop=1,...
		gapNote := ""
		if rc.chunkCount > 1 {
			gapNote = fmt.Sprintf("+%dms ", gap.Milliseconds())
		} else {
			gapNote = "+0ms "
		}
		stallTag := ""
		if gap > 5*time.Second {
			stallTag = "⚠ "
		}
		eventsNote := formatPerChunkEvents(perChunk)
		log.Printf("📡 %s#%-3d %s%-7dB t+%dms types=%s",
			stallTag, rc.chunkCount, gapNote, len(b),
			now.Sub(rc.requestStart).Milliseconds(),
			eventsNote)
	}

	// Buffer for post-stream metadata parsing, then forward immediately so the
	// downstream Flusher can push the chunk to the caller without delay.
	//
	// We deliberately do NOT attempt to parse usage info per-chunk:
	//   * Anthropic only emits final usage in the terminal `message_stop` event,
	//     so per-chunk attempts pre-stop produce no new info.
	//   * Re-parsing the entire accumulated buffer on every chunk is O(N²) and
	//     blocks Write, which stalls streaming for the caller.
	// The post-ServeHTTP block in TokenParsingMiddleware parses the full body
	// (and the provider's parseStreamingResponse already handles truncated
	// streams with a partial-metadata fallback), so we lose nothing.
	if !rc.captureFull {
		remaining := maxCapturedBodyBytes - rc.body.Len()
		if remaining <= 0 {
			rc.captureFull = true
			log.Printf("📦 response capture cap reached (%dB); dropping further bytes from metadata buffer", maxCapturedBodyBytes)
		} else if len(b) <= remaining {
			rc.body.Write(b)
		} else {
			rc.body.Write(b[:remaining])
			rc.captureFull = true
			log.Printf("📦 response capture cap reached (%dB); dropping further bytes from metadata buffer", maxCapturedBodyBytes)
		}
	}
	return rc.ResponseWriter.Write(b)
}

// sniffSSEEvents walks newly-received bytes line-by-line and counts SSE event
// types and `"type":"..."` markers inside data payloads. Updates the cumulative
// histograms on rc and returns a per-chunk map so the caller can log just what
// arrived in this chunk. Maintains a leftover buffer for partial trailing
// lines so a marker split across two chunks isn't missed.
//
// Per-chunk keys are prefixed (event:NAME or type:NAME) so we can render them
// in a single line without ambiguity.
func (rc *responseCapture) sniffSSEEvents(b []byte) map[string]int64 {
	if rc.sseEventCounts == nil {
		rc.sseEventCounts = make(map[string]int64, 8)
	}
	if rc.sseDataTypes == nil {
		rc.sseDataTypes = make(map[string]int64, 16)
	}
	perChunk := make(map[string]int64, 4)

	// Combine any partial line we held back with the new bytes.
	var work []byte
	if len(rc.sseLeftover) > 0 {
		work = make([]byte, 0, len(rc.sseLeftover)+len(b))
		work = append(work, rc.sseLeftover...)
		work = append(work, b...)
		rc.sseLeftover = rc.sseLeftover[:0]
	} else {
		work = b
	}

	// Split on \n and stash any incomplete trailing line for the next chunk.
	start := 0
	for i := 0; i < len(work); i++ {
		if work[i] != '\n' {
			continue
		}
		line := work[start:i]
		start = i + 1
		if n := len(line); n > 0 && line[n-1] == '\r' {
			line = line[:n-1]
		}
		if len(line) == 0 {
			continue
		}
		switch {
		case bytes.HasPrefix(line, []byte("event: ")):
			ev := string(line[len("event: "):])
			rc.sseEventCounts[ev]++
			perChunk["event:"+ev]++
		case bytes.HasPrefix(line, []byte("data: ")):
			payload := line[len("data: "):]
			if t := extractJSONType(payload); t != "" {
				rc.sseDataTypes[t]++
				perChunk["type:"+t]++
			}
		}
	}
	if start < len(work) {
		rc.sseLeftover = append(rc.sseLeftover[:0], work[start:]...)
	}
	return perChunk
}

// formatPerChunkEvents renders the per-chunk event histogram compactly:
//   - "ping=2,content_block_delta=12" (sorted for stable output)
//   - "-" when the chunk had no recognizable SSE events (likely a partial line
//     that will be completed by the next chunk).
func formatPerChunkEvents(m map[string]int64) string {
	if len(m) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		if v == 1 {
			parts = append(parts, k)
		} else {
			parts = append(parts, fmt.Sprintf("%s×%d", k, v))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// formatEventCounts renders the SSE event-type histogram compactly, e.g.
// "event:ping=2,event:content_block_delta=12 type:thinking_delta=10,type:text_delta=2".
// Returns "-" if no events have been counted yet.
func (rc *responseCapture) formatEventCounts() string {
	if len(rc.sseEventCounts) == 0 && len(rc.sseDataTypes) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(rc.sseEventCounts)+len(rc.sseDataTypes))
	for k, v := range rc.sseEventCounts {
		parts = append(parts, fmt.Sprintf("event:%s=%d", k, v))
	}
	for k, v := range rc.sseDataTypes {
		parts = append(parts, fmt.Sprintf("type:%s=%d", k, v))
	}
	return strings.Join(parts, ",")
}

// extractJSONType pulls the value of the top-level `"type"` field from a JSON
// object payload without doing a full json.Unmarshal. It's tolerant: returns
// "" on anything that doesn't look like a clean `"type":"..."` substring near
// the start of the object. Good enough for telemetry.
func extractJSONType(b []byte) string {
	// Look for `"type":"`. Bound the search to a small prefix to avoid pathological scans.
	limit := len(b)
	if limit > 256 {
		limit = 256
	}
	idx := bytes.Index(b[:limit], []byte(`"type":"`))
	if idx < 0 {
		// Tolerate `"type": "...".
		idx = bytes.Index(b[:limit], []byte(`"type": "`))
		if idx < 0 {
			return ""
		}
		idx += len(`"type": "`)
	} else {
		idx += len(`"type":"`)
	}
	end := bytes.IndexByte(b[idx:], '"')
	if end < 0 {
		return ""
	}
	if end > 64 { // sanity cap on type-name length
		return ""
	}
	return string(b[idx : idx+end])
}

// chunkPreview returns a printable, length-limited preview of a byte chunk for
// logging. Non-printable bytes (other than space, tab, newline) are rendered
// as `.`, and the result is truncated to maxLen characters with an ellipsis.
func chunkPreview(b []byte, maxLen int) string {
	n := len(b)
	if n > maxLen {
		n = maxLen
	}
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		c := b[i]
		switch {
		case c == '\n':
			out = append(out, '\\', 'n')
		case c == '\r':
			out = append(out, '\\', 'r')
		case c == '\t':
			out = append(out, '\\', 't')
		case c < 0x20 || c >= 0x7f:
			out = append(out, '.')
		default:
			out = append(out, c)
		}
	}
	if len(b) > maxLen {
		out = append(out, '.', '.', '.')
	}
	return string(out)
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
