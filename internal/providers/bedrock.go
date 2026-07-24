package providers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/internal/proxylog"
	eventstream "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/gorilla/mux"
)

// Bedrock provider — transparent SigV4 passthrough.
//
// Unlike the other providers, the Bedrock proxy does NOT mint or validate any
// credentials. AWS Bedrock authenticates by SigV4 (request-signed, not static
// API key) and the canonical request hashes the body's `x-amz-content-sha256`
// header. Mutating the body, the path, the Host, or any signed header would
// invalidate the signature, so this proxy is intentionally byte-for-byte:
//
//   - strips its own `/bedrock` URL prefix so the upstream sees the path the
//     client signed (`/model/{modelId}/converse[-stream]`);
//   - sets `req.Host` to the canonical AWS hostname (preserves what the
//     client signed against);
//   - leaves `Authorization`, `x-amz-date`, `x-amz-content-sha256`, and the
//     request body untouched.
//
// Clients sign with whatever AWS identity they already have (IAM role, IRSA,
// env vars, AssumeRole, etc.). See `examples/bedrock-passthrough/python.py`
// for the boto3 `before-send` hook recipe that rewrites the destination URL
// after the signer has finished.
const (
	defaultBedrockRegion   = "us-west-2"
	bedrockEventStreamMIME = "application/vnd.amazon.eventstream"
)

// BedrockProxy implements the Provider interface for AWS Bedrock Converse.
//
// The proxy is region-pinned at startup (via $AWS_REGION, default
// `us-west-2`). Cross-region inference profiles (`us.anthropic.*`) still
// work because AWS routes them server-side from any us-* endpoint — the
// proxy itself only needs one canonical upstream URL.
type BedrockProxy struct {
	proxy   *httputil.ReverseProxy
	region  string
	baseURL string
}

// NewBedrockProxy creates a new Bedrock reverse proxy.
func NewBedrockProxy(opts ...ProxyOptions) *BedrockProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = defaultBedrockRegion
	}
	baseURL := fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	targetURL, err := url.Parse(baseURL)
	if err != nil {
		// baseURL is composed from a constant + region; parse failure
		// indicates a malformed AWS_REGION value at startup. Panic so the
		// stack trace points at the misconfiguration, instead of silently
		// log.Fatalf-ing.
		panic(fmt.Sprintf("invalid Bedrock upstream URL %q (region=%q): %v", baseURL, region, err))
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	bedrockProxy := &BedrockProxy{proxy: proxy, region: region, baseURL: baseURL}

	// Director: strip our `/bedrock` URL prefix and pin the Host header to the
	// canonical AWS hostname so the SigV4-signed Authorization remains valid.
	// We deliberately do NOT call the shared CreateGenericDirector helper here:
	// that one logs through provider.IsStreamingRequest(), which works for
	// path-based detection but isn't useful for Bedrock (path suffix is
	// already enough). We also need precise control over Host header rewriting.
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// After originalDirector, req.URL.Path is "/bedrock/model/..." — strip
		// our prefix to match what the client signed.  We must also strip from
		// RawPath when it is set, because Bedrock model IDs contain `:` (e.g.
		// `us.anthropic.claude-sonnet-4-5-...v1:0`) which boto3 URL-encodes to
		// `%3A0` before signing.  If we mutate Path but not RawPath, Go's
		// net/url.EscapedPath falls back to re-escaping the decoded Path and
		// emits `:` verbatim — making the on-wire path differ from the signed
		// canonical path, and SigV4 fails with InvalidSignatureException.
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/bedrock")
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, "/bedrock")
		}
		// Force the Host header that SigV4 signed against (without this Go's
		// transport derives Host from req.URL.Host which already equals
		// targetURL.Host, but being explicit guards future refactors).
		req.Host = targetURL.Host
		// Optional: strip Accept-Encoding so upstream returns plain bytes —
		// matches the debug-mode contract of CreateGenericDirector.
		// boto3 adds Accept-Encoding after signing so deleting it is safe
		// there, but some signers (AWS SDK for Java v2, aws-crt-based
		// custom signers) include every present header in SignedHeaders;
		// deleting a signed header changes the canonical request AWS
		// reconstructs and every request fails with
		// InvalidSignatureException. Only strip when the client did not
		// sign it.
		if opt.DisableGzip && !sigV4HeaderSigned(req.Header.Get("Authorization"), "accept-encoding") {
			req.Header.Del("Accept-Encoding")
		}
		isStreaming := bedrockProxy.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying bedrock streaming request: %s %s", req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying bedrock request: %s %s", req.Method, req.URL.Path)
		}
	}

	proxy.Transport = newProxyTransport(opt.DisableGzip, opt.ResponseHeaderTimeout)

	proxy.ModifyResponse = func(resp *http.Response) error {
		if bedrockProxy.isStreamingResponse(resp) {
			log.Printf("Detected streaming response from Bedrock")
			resp.Header.Set("Cache-Control", "no-cache")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Set("X-Accel-Buffering", "no")
			resp.Header.Del("Content-Length")
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		proxylog.Upstream("bedrock reverse proxy transport error: %v", err)
		if bedrockProxy.IsStreamingRequest(r) {
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", bedrockEventStreamMIME)
				w.Header().Set(proxylog.HeaderErrorSource, proxylog.ErrorSourceUpstream)
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, proxylog.UpstreamPlain("bedrock transport: %v", err))
			}
			return
		}
		proxylog.WriteUpstreamJSONError(w, http.StatusBadGateway, fmt.Sprintf("bedrock transport: %v", err))
	}

	return bedrockProxy
}

// GetName implements Provider.
func (b *BedrockProxy) GetName() string { return "bedrock" }

// Region returns the upstream Bedrock region the proxy is bound to. Exposed
// for /health and for tests; not part of the Provider interface.
func (b *BedrockProxy) Region() string { return b.region }

// IsStreamingRequest implements Provider. Bedrock's Converse API exposes a
// dedicated `*-stream` path, so the streaming detection is path-based — no
// JSON body parsing is needed (and parsing the body would unnecessarily
// burn the GetBody side-channel for callers that did not set it up).
// IsStreamingRequest assumes the caller has already routed to Bedrock; cross-
// provider gating lives in ProviderManager.IsStreamingRequest.
func (b *BedrockProxy) IsStreamingRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if strings.Contains(req.Header.Get("Accept"), bedrockEventStreamMIME) {
		return true
	}
	p := req.URL.Path
	return strings.HasSuffix(p, "/converse-stream") || strings.HasSuffix(p, "/invoke-with-response-stream")
}

func (b *BedrockProxy) isStreamingResponse(resp *http.Response) bool {
	return strings.Contains(resp.Header.Get("Content-Type"), bedrockEventStreamMIME)
}

// Proxy implements Provider.
func (b *BedrockProxy) Proxy() http.Handler { return b.proxy }

// WrapTransport replaces the proxy's transport with fn(current transport).
// Used by the circuit-breaker wrapping in main.go.
func (b *BedrockProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	b.proxy.Transport = fn(b.proxy.Transport)
}

// GetHealthStatus implements Provider.
func (b *BedrockProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          "bedrock",
		"status":            "healthy",
		"baseURL":           b.baseURL,
		"region":            b.region,
		"streaming_support": true,
		"body_parsing":      true,
		"auth":              "client_sigv4_passthrough",
	}
}

// BedrockConverseUsage models the `usage` block in a Bedrock Converse
// response or `metadata` eventstream payload. Field names mirror AWS's
// camelCase wire format.
type BedrockConverseUsage struct {
	InputTokens              int `json:"inputTokens"`
	OutputTokens             int `json:"outputTokens"`
	TotalTokens              int `json:"totalTokens"`
	CacheReadInputTokens     int `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int `json:"cacheWriteInputTokens,omitempty"`
}

// BedrockConverseResponse models a non-streaming Converse response body.
// Only the fields we care about (usage, stopReason) are typed.
type BedrockConverseResponse struct {
	StopReason string               `json:"stopReason"`
	Usage      BedrockConverseUsage `json:"usage"`
}

// ParseResponseMetadata implements Provider.
//
// Bedrock responses do not echo back the model name (the model is in the URL
// the client signed against), so Model is left empty here. TokenParsingMiddleware
// fills it in by calling ExtractRequestModelAndMessages on the matched request.
func (b *BedrockProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	if isStreaming {
		return b.parseStreamingResponse(responseBody)
	}
	return b.parseNonStreamingResponse(responseBody)
}

func (b *BedrockProxy) parseNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	decompressed, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress bedrock response: %w", err)
	}
	if gz, ok := decompressed.(*gzip.Reader); ok {
		defer gz.Close()
	}
	body, err := io.ReadAll(decompressed)
	if err != nil {
		return nil, fmt.Errorf("failed to read bedrock response: %w", err)
	}
	var r BedrockConverseResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("failed to parse bedrock response: %w", err)
	}
	if r.Usage.InputTokens == 0 && r.Usage.OutputTokens == 0 {
		// InvokeModel (`/model/{id}/invoke`) returns the model-NATIVE body,
		// not the Converse shape. Anthropic models report snake_case usage
		// (Go's JSON matching is case-insensitive but not
		// underscore-insensitive, so input_tokens never matches inputTokens)
		// — without this fallback the whole InvokeModel API family silently
		// recorded zero tokens.
		var native struct {
			StopReason string `json:"stop_reason"`
			Usage      struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(body, &native) == nil &&
			(native.Usage.InputTokens > 0 || native.Usage.OutputTokens > 0) {
			return &LLMResponseMetadata{
				InputTokens:              native.Usage.InputTokens,
				OutputTokens:             native.Usage.OutputTokens,
				TotalTokens:              native.Usage.InputTokens + native.Usage.OutputTokens,
				CacheReadInputTokens:     native.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: native.Usage.CacheCreationInputTokens,
				Provider:                 "bedrock",
				IsStreaming:              false,
				FinishReason:             native.StopReason,
			}, nil
		}
	}
	total := r.Usage.TotalTokens
	if total == 0 {
		total = r.Usage.InputTokens + r.Usage.OutputTokens
	}
	return &LLMResponseMetadata{
		InputTokens:              r.Usage.InputTokens,
		OutputTokens:             r.Usage.OutputTokens,
		TotalTokens:              total,
		CacheReadInputTokens:     r.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: r.Usage.CacheCreationInputTokens,
		Provider:                 "bedrock",
		IsStreaming:              false,
		FinishReason:             r.StopReason,
	}, nil
}

// parseStreamingResponse decodes the AWS event-stream framing
// (`application/vnd.amazon.eventstream`) until it finds the terminal
// `metadata` event, which carries the final `usage` payload. We also
// pick up `stopReason` from `messageStop` for the finish reason field.
//
// The eventstream package handles all the binary prelude / CRC / header
// validation work; we only inspect headers and JSON-decode the payload.
func (b *BedrockProxy) parseStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	decompressed, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress bedrock streaming response: %w", err)
	}
	if gz, ok := decompressed.(*gzip.Reader); ok {
		defer gz.Close()
	}

	decoder := eventstream.NewDecoder()
	// Reusable scratch buffer. The eventstream decoder grows it as needed,
	// but pre-sizing avoids early reallocs for typical payloads.
	payloadBuf := make([]byte, 0, 16*1024)

	metadata := &LLMResponseMetadata{
		Provider:    "bedrock",
		IsStreaming: true,
	}
	sawData := false
	sawUsage := false

	for {
		msg, decErr := decoder.Decode(decompressed, payloadBuf)
		if decErr != nil {
			// ErrUnexpectedEOF is a stream cut MID-frame (the typical shape
			// when a client disconnects during body copy). Treat it like a
			// clean EOF so the partial-metadata fallback below still returns
			// whatever was decoded (e.g. the finish reason) instead of
			// discarding everything.
			if errors.Is(decErr, io.EOF) || errors.Is(decErr, io.ErrUnexpectedEOF) {
				break
			}
			return nil, fmt.Errorf("decode eventstream: %w", decErr)
		}
		sawData = true

		// Headers we care about:
		//   :event-type (typed eventstream header)
		//   :message-type ("event" / "exception" / "error")
		eventType := eventstreamHeaderString(msg.Headers, ":event-type")
		messageType := eventstreamHeaderString(msg.Headers, ":message-type")
		if messageType != "" && messageType != "event" {
			// Exception / error frames carry diagnostic JSON in the payload.
			// We treat them as terminal: log and stop so the caller can
			// surface a partial-metadata fallback.
			log.Printf("🪶 Bedrock eventstream non-event message_type=%s event_type=%s payload=%s",
				messageType, eventType, string(msg.Payload[:min(200, len(msg.Payload))]))
			break
		}

		switch eventType {
		case "messageStop":
			var stop struct {
				StopReason string `json:"stopReason"`
			}
			if e := json.Unmarshal(msg.Payload, &stop); e == nil {
				if stop.StopReason != "" {
					metadata.FinishReason = stop.StopReason
				}
			}
		case "metadata":
			// Bedrock's terminal `metadata` event is the only one that carries
			// `usage`. We can stop iterating after we see it — but we keep
			// looping in case AWS ever emits trailing frames after metadata
			// (it currently does not).
			var meta struct {
				Usage BedrockConverseUsage `json:"usage"`
			}
			if e := json.Unmarshal(msg.Payload, &meta); e == nil {
				metadata.InputTokens = meta.Usage.InputTokens
				metadata.OutputTokens = meta.Usage.OutputTokens
				total := meta.Usage.TotalTokens
				if total == 0 {
					total = meta.Usage.InputTokens + meta.Usage.OutputTokens
				}
				metadata.TotalTokens = total
				metadata.CacheReadInputTokens = meta.Usage.CacheReadInputTokens
				metadata.CacheCreationInputTokens = meta.Usage.CacheCreationInputTokens
				sawUsage = true
			}
		case "chunk":
			// InvokeModelWithResponseStream frames: the payload is
			// {"bytes":"<base64 model-native JSON>"} (Converse streams never
			// emit this event type). Without handling it, the InvokeModel
			// streaming family silently recorded zero tokens.
			if b.parseInvokeChunk(msg.Payload, metadata) {
				sawUsage = true
			}
		}
	}
	if sawUsage && metadata.TotalTokens == 0 {
		metadata.TotalTokens = metadata.InputTokens + metadata.OutputTokens
	}

	if !sawData {
		return nil, fmt.Errorf("no eventstream frames decoded from bedrock streaming response")
	}
	if !sawUsage {
		// Stream truncated before the `metadata` event arrived. Return a
		// partial-metadata envelope (matches Anthropic's behaviour) so the
		// caller sees the request happened even when token counts aren't
		// available.
		return metadata, nil
	}
	return metadata, nil
}

// parseInvokeChunk decodes one InvokeModelWithResponseStream `chunk` frame
// and folds any usage/finish-reason information into metadata. It returns
// true when it found authoritative token counts.
//
// Two sources of truth, in increasing authority:
//   - Anthropic-native stream events (message_start seeds input/cache
//     tokens; message_delta carries the CUMULATIVE output count and the
//     stop reason);
//   - the `amazon-bedrock-invocationMetrics` block Bedrock appends to the
//     final chunk for EVERY model family, which we prefer since it is
//     model-agnostic.
func (b *BedrockProxy) parseInvokeChunk(payload []byte, metadata *LLMResponseMetadata) bool {
	var wrapper struct {
		Bytes []byte `json:"bytes"` // encoding/json base64-decodes into []byte
	}
	if json.Unmarshal(payload, &wrapper) != nil || len(wrapper.Bytes) == 0 {
		return false
	}

	var inner struct {
		Type    string `json:"type"`
		Message *struct {
			Usage struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Delta *struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage *struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		InvocationMetrics *struct {
			InputTokenCount  int `json:"inputTokenCount"`
			OutputTokenCount int `json:"outputTokenCount"`
		} `json:"amazon-bedrock-invocationMetrics"`
	}
	if json.Unmarshal(wrapper.Bytes, &inner) != nil {
		return false
	}

	sawUsage := false
	switch inner.Type {
	case "message_start":
		if inner.Message != nil {
			u := inner.Message.Usage
			if u.InputTokens > 0 {
				metadata.InputTokens = u.InputTokens
			}
			if u.OutputTokens > 0 {
				metadata.OutputTokens = u.OutputTokens
			}
			metadata.CacheReadInputTokens = u.CacheReadInputTokens
			metadata.CacheCreationInputTokens = u.CacheCreationInputTokens
			sawUsage = u.InputTokens > 0 || u.OutputTokens > 0
		}
	case "message_delta":
		if inner.Delta != nil && inner.Delta.StopReason != "" {
			metadata.FinishReason = inner.Delta.StopReason
		}
		if inner.Usage != nil && inner.Usage.OutputTokens > 0 {
			metadata.OutputTokens = inner.Usage.OutputTokens
			sawUsage = true
		}
	}
	if m := inner.InvocationMetrics; m != nil && (m.InputTokenCount > 0 || m.OutputTokenCount > 0) {
		metadata.InputTokens = m.InputTokenCount
		metadata.OutputTokens = m.OutputTokenCount
		metadata.TotalTokens = m.InputTokenCount + m.OutputTokenCount
		sawUsage = true
	}
	return sawUsage
}

// sigV4HeaderSigned reports whether name appears in the SignedHeaders list of
// an AWS SigV4 Authorization header value ("AWS4-HMAC-SHA256
// Credential=..., SignedHeaders=a;b;c, Signature=..."). SignedHeaders
// entries are lowercase per the SigV4 spec, but compare case-insensitively
// to be safe.
func sigV4HeaderSigned(authorization, name string) bool {
	const marker = "SignedHeaders="
	idx := strings.Index(authorization, marker)
	if idx < 0 {
		return false
	}
	list := authorization[idx+len(marker):]
	if end := strings.IndexAny(list, ", "); end >= 0 {
		list = list[:end]
	}
	for _, h := range strings.Split(list, ";") {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// eventstreamHeaderString returns the string value of a given header name,
// or "" if absent or non-string. eventstream.Value.String() panics on nil,
// so we range Headers and only read from a present entry.
func eventstreamHeaderString(hs eventstream.Headers, name string) string {
	for _, h := range hs {
		if h.Name == name && h.Value != nil {
			return h.Value.String()
		}
	}
	return ""
}

// UserIDFromRequest implements Provider. Bedrock does not carry a user
// identity in the request body; the IAM principal (which the proxy never
// sees in plaintext) is the authoritative caller. Return empty and let
// the middleware fall back to header / IP-based identification.
func (b *BedrockProxy) UserIDFromRequest(req *http.Request) string {
	return ""
}

// ValidateAPIKey implements Provider. The Bedrock proxy is a transparent
// SigV4 passthrough — the `Authorization` header is a signed request that
// only AWS can validate. We never inspect or rewrite it.
func (b *BedrockProxy) ValidateAPIKey(req *http.Request, _ APIKeyStore) error {
	return nil
}

// RegisterExtraRoutes implements Provider. The default `/bedrock/...` catch-
// all already covers Converse paths, but we register an explicit
// `/bedrock/model/{modelId}` PathPrefix so logs and mux variable extraction
// reflect the model dimension when we ever want to read it from the router.
func (b *BedrockProxy) RegisterExtraRoutes(router *mux.Router) {
	router.PathPrefix("/bedrock/model/").Handler(b.Proxy()).Methods("POST", "OPTIONS")
	router.PathPrefix("/model/").Handler(b.Proxy()).Methods("POST", "OPTIONS")
}

// ExtractRequestModelAndMessages implements Provider.
//
// The model name lives in the URL path (`/model/{modelId}/converse[-stream]`),
// not the body. We always return the path-extracted model, which is enough
// for cost tracking and circuit-breaker keying. For token estimation we
// optionally parse the Converse body (Content-Type: application/json) and
// pull `messages[].content[].text` while restoring req.Body byte-identically.
func (b *BedrockProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	if req == nil || req.URL == nil {
		return "", nil
	}
	model := ExtractBedrockModelFromPath(req.URL.Path)
	if model == "" {
		return "", nil
	}
	if req.Method != "POST" || req.Body == nil {
		return model, nil
	}
	if ct := req.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "application/json") {
		return model, nil
	}

	bodyBytes, err := readAndRestoreBedrockBody(req)
	if err != nil || len(bodyBytes) == 0 {
		return model, nil
	}
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return model, nil
	}

	messages := make([]string, 0, 8)
	if rawMsgs, ok := data["messages"].([]interface{}); ok {
		for _, m := range rawMsgs {
			msg, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			if parts, ok := msg["content"].([]interface{}); ok {
				for _, p := range parts {
					pm, ok := p.(map[string]interface{})
					if !ok {
						continue
					}
					if t, ok := pm["text"].(string); ok && t != "" {
						messages = append(messages, t)
					}
				}
			}
		}
	}
	// System messages live in a top-level `system: [{text: "..."}]` block
	// in the Converse API. They count toward input tokens too.
	if sys, ok := data["system"].([]interface{}); ok {
		for _, s := range sys {
			sm, ok := s.(map[string]interface{})
			if !ok {
				continue
			}
			if t, ok := sm["text"].(string); ok && t != "" {
				messages = append(messages, t)
			}
		}
	}
	return model, messages
}

// ExtractBedrockModelFromPath parses a Bedrock Converse path like
//
//	/bedrock/model/{modelId}/converse
//	/bedrock/model/{modelId}/converse-stream
//	/model/{modelId}/converse
//
// returning the URL-decoded modelId. Returns "" when the path doesn't match.
// Exported for use by the circuit-breaker model extractor in main.go.
func ExtractBedrockModelFromPath(path string) string {
	s := strings.TrimPrefix(path, "/bedrock")
	if !strings.HasPrefix(s, "/model/") {
		return ""
	}
	rest := s[len("/model/"):]
	idx := strings.Index(rest, "/")
	if idx <= 0 {
		return ""
	}
	rawID := rest[:idx]
	decoded, err := url.PathUnescape(rawID)
	if err != nil {
		return rawID
	}
	return decoded
}

// readAndRestoreBedrockBody reads req.Body fully and restores req.Body / GetBody
// so the SigV4-hashed bytes downstream are byte-identical. The same pattern
// is used in the OpenAI/Anthropic providers via their private helpers; we
// duplicate here rather than exporting from those files to keep providers
// independently mock-able.
func readAndRestoreBedrockBody(req *http.Request) ([]byte, error) {
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
	}
	return bodyBytes, nil
}
