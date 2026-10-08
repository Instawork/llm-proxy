package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/gorilla/mux"
)

const (
	openRouterName = "openrouter"
	// openRouterBaseURL is the upstream host. The OpenRouter API lives under
	// /api/v1, so clients point their SDK base URL at /openrouter/api/v1 and
	// the generic rewrite forwards /api/v1/... unchanged after stripping the
	// /openrouter prefix.
	openRouterBaseURL = "https://openrouter.ai"
	// OpenRouterUpstreamPathPrefix is the path prefix of rewritten upstream
	// requests. No other provider's upstream API lives under it.
	OpenRouterUpstreamPathPrefix = "/api/v1/"
)

// OpenRouterProxy forwards OpenAI-compatible Chat Completions traffic to
// OpenRouter. Responses are OpenAI-shaped, so metering reuses the shared
// OpenAI-format parser attributed to "openrouter". Model ids carry a vendor
// namespace (e.g. "deepseek/deepseek-v4-pro") and travel only in the JSON
// body, never in the URL path.
type OpenRouterProxy struct {
	proxy *httputil.ReverseProxy
}

// NewOpenRouterProxy creates a new OpenRouter reverse proxy.
func NewOpenRouterProxy(opts ...ProxyOptions) *OpenRouterProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	targetURL, err := url.Parse(openRouterBaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid openRouterBaseURL constant %q: %v", openRouterBaseURL, err))
	}

	proxy := &httputil.ReverseProxy{}
	openRouterProxy := &OpenRouterProxy{proxy: proxy}

	proxy.Rewrite = CreateGenericRewrite(openRouterProxy, targetURL, opt.DisableGzip)
	proxy.Transport = newProxyTransport(opt.DisableGzip, opt.ResponseHeaderTimeout)

	proxy.ModifyResponse = func(resp *http.Response) error {
		if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			resp.Header.Set("Cache-Control", "no-cache")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Set("X-Accel-Buffering", "no")
			resp.Header.Del("Content-Length")
		}
		return nil
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		proxylog.Upstream("openrouter reverse proxy transport error: %v", err)
		if !openRouterProxy.IsStreamingRequest(r) {
			proxylog.WriteUpstreamJSONError(w, http.StatusBadGateway, fmt.Sprintf("openrouter transport: %v", err))
			return
		}
		if w.Header().Get("Content-Type") != "" {
			proxylog.Proxy("openrouter cannot send error response, headers already sent")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set(proxylog.HeaderErrorSource, proxylog.ErrorSourceUpstream)
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, proxylog.UpstreamSSEDataLine("openrouter transport: %v", err))
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}

	return openRouterProxy
}

// GetName returns the provider name, which is also its route prefix.
func (o *OpenRouterProxy) GetName() string {
	return openRouterName
}

// IsStreamingRequest reports whether the request asks for an SSE stream,
// either via the Accept header or "stream": true on a completions body.
func (o *OpenRouterProxy) IsStreamingRequest(req *http.Request) bool {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	if req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/completions") {
		return requestBodyHasStreamTrue(req, openRouterName)
	}
	return false
}

// ParseResponseMetadata extracts tokens and model from OpenAI-shaped
// OpenRouter responses (JSON or SSE).
func (o *OpenRouterProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	return parseOpenAIFormatMetadata(responseBody, isStreaming, openRouterName)
}

// Proxy returns the HTTP handler for the OpenRouter provider.
func (o *OpenRouterProxy) Proxy() http.Handler {
	return o.proxy
}

// WrapTransport replaces the proxy's transport with fn(current transport),
// used to layer the fake upstream, circuit breaker and body-read logging.
func (o *OpenRouterProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	o.proxy.Transport = fn(o.proxy.Transport)
}

// GetHealthStatus returns the health status of the OpenRouter proxy.
func (o *OpenRouterProxy) GetHealthStatus() map[string]any {
	return map[string]any{
		"provider":          openRouterName,
		"status":            "healthy",
		"baseURL":           openRouterBaseURL,
		"streaming_support": true,
		"body_parsing":      true,
	}
}

// UserIDFromRequest returns the OpenAI-style "user" field from the body.
func (o *OpenRouterProxy) UserIDFromRequest(req *http.Request) string {
	if req.Body == nil || req.Method != http.MethodPost || !strings.HasPrefix(req.URL.Path, "/"+openRouterName+"/") {
		return ""
	}
	body, err := readAndRestoreOpenRouterBody(req)
	if err != nil {
		proxylog.Proxy("openrouter user ID extraction: error reading request body: %v", err)
		return ""
	}
	if len(body) == 0 {
		return ""
	}
	var probe struct {
		User json.RawMessage `json:"user"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		proxylog.Proxy("openrouter user ID extraction: error parsing request JSON: %v", err)
		return ""
	}
	var user string
	if len(probe.User) > 0 && json.Unmarshal(probe.User, &user) == nil {
		return user
	}
	return ""
}

// RegisterExtraRoutes is a no-op: OpenRouter is served only under /openrouter/.
func (o *OpenRouterProxy) RegisterExtraRoutes(_ *mux.Router) {}

// ExtractRequestModelAndMessages returns the model and message text of an
// OpenAI-shaped request body for token estimation. Restores req.Body.
// Accepts both the inbound /openrouter/... path and the rewritten upstream
// /api/v1/... path that the circuit breaker transport sees.
func (o *OpenRouterProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	if req == nil || req.Method != http.MethodPost {
		return "", nil
	}
	if path := req.URL.Path; !strings.HasPrefix(path, "/"+openRouterName+"/") && !strings.HasPrefix(path, OpenRouterUpstreamPathPrefix) {
		return "", nil
	}
	body, err := readAndRestoreOpenRouterBody(req)
	if err != nil || len(body) == 0 {
		return "", nil
	}
	return extractOpenAIModelAndMessages(body)
}

// ValidateAPIKey translates an llm-proxy key in the bearer Authorization
// header into the stored OpenRouter credential, rejecting keys issued for
// another provider.
func (o *OpenRouterProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	const bearerPrefix = "Bearer "
	authHeader := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return nil
	}
	apiKey := strings.TrimPrefix(authHeader, bearerPrefix)

	actualKey, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), apiKey)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}
	if provider != "" && provider != openRouterName {
		return fmt.Errorf("API key is for provider %s, not %s", provider, openRouterName)
	}
	if actualKey != apiKey {
		req.Header.Set("Authorization", bearerPrefix+actualKey)
		log.Printf("🔑 OpenRouter: Translated API key from iw: format")
	}
	return nil
}

func readAndRestoreOpenRouterBody(req *http.Request) ([]byte, error) {
	if req.GetBody != nil {
		r, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return io.ReadAll(r)
	}
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return body, nil
}
