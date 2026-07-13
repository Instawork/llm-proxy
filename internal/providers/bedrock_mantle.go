package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/gorilla/mux"
)

const (
	bedrockMantleName    = "bedrock-mantle"
	bedrockMantleService = "bedrock-mantle"
	// defaultBedrockMantleAnthropicRegion is where Claude models are served on
	// the Bedrock Mantle `/anthropic/v1/messages` endpoint. Anthropic Mantle
	// SKUs are provisioned in us-east-1 for our account, while the OpenAI
	// Mantle SKUs (gpt-5.x) live in the default us-west-2 region — signing an
	// Anthropic request for us-west-2 returns a 500 api_error. Overridable via
	// the BEDROCK_MANTLE_ANTHROPIC_REGION env var or ProxyOptions.
	defaultBedrockMantleAnthropicRegion = "us-east-1"
)

// BedrockMantleProxy forwards OpenAI- and Anthropic-compatible requests to AWS
// Bedrock Mantle. It authenticates callers with llm-proxy API keys, then signs
// the rewritten upstream request with the task's AWS credential chain.
type BedrockMantleProxy struct {
	proxy   *httputil.ReverseProxy
	region  string
	baseURL string
	// anthropicRegion is the region used for `/anthropic/v1/messages` requests.
	// Claude Mantle SKUs are provisioned in a different region than the OpenAI
	// SKUs, so Anthropic traffic is retargeted (host + SigV4) to this region.
	anthropicRegion string
	// modelProjects maps a model id (or alias) to the Bedrock project id sent
	// as the OpenAI-Project header. Empty entries (or an absent model) leave the
	// account-level data-retention policy in force. Read-only after construction.
	modelProjects map[string]string
}

// NewBedrockMantleProxy creates a Bedrock Mantle proxy using AWS credentials
// from the shared config profile (SSO, ~/.aws/credentials, etc.). The profile
// chain is used explicitly so docker-compose's DynamoDB Local static keys do
// not override real AWS credentials for upstream SigV4.
func NewBedrockMantleProxy(opts ...ProxyOptions) *BedrockMantleProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = defaultBedrockRegion
	}
	if opt.MantleAnthropicRegion == "" {
		if envRegion := os.Getenv("BEDROCK_MANTLE_ANTHROPIC_REGION"); envRegion != "" {
			opt.MantleAnthropicRegion = envRegion
		} else {
			opt.MantleAnthropicRegion = defaultBedrockMantleAnthropicRegion
		}
	}
	cfg, err := loadBedrockMantleAWSConfig(context.Background(), region)
	if err != nil {
		panic(fmt.Sprintf("load AWS config for Bedrock Mantle: %v", err))
	}
	return newBedrockMantleProxy(region, cfg.Credentials, opt)
}

func loadBedrockMantleAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	profile := os.Getenv("BEDROCK_AWS_PROFILE")
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	if profile == "" {
		profile = "default"
	}
	return awsconfig.LoadDefaultConfig(
		ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithSharedConfigProfile(profile),
	)
}

func newBedrockMantleProxy(region string, credentials aws.CredentialsProvider, opt ProxyOptions) *BedrockMantleProxy {
	baseURL := fmt.Sprintf("https://bedrock-mantle.%s.api.aws", region)
	targetURL, err := url.Parse(baseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid Bedrock Mantle upstream URL %q: %v", baseURL, err))
	}

	anthropicRegion := opt.MantleAnthropicRegion
	if anthropicRegion == "" {
		anthropicRegion = region
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	mantle := &BedrockMantleProxy{proxy: proxy, region: region, baseURL: baseURL, anthropicRegion: anthropicRegion, modelProjects: opt.MantleModelProjects}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/"+bedrockMantleName)
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, "/"+bedrockMantleName)
		}
		originalDirector(req)
		// Retarget Anthropic Messages traffic to the region where Claude Mantle
		// SKUs are provisioned. OpenAI traffic keeps the default region host.
		host := targetURL.Host
		if regionHost := mantle.hostForPath(req.URL.Path); regionHost != "" {
			host = regionHost
			req.URL.Host = regionHost
		}
		req.Host = host
		// Bedrock Mantle's Anthropic Messages surface rejects any
		// ``anthropic-beta`` flag with a 400 ("invalid beta flag") — the
		// features gated behind those flags on the first-party API (e.g. GA
		// tool search) are simply built in on Mantle. Anthropic SDK clients
		// (and langchain_anthropic) auto-attach the header when they detect a
		// tool-search builtin, so strip it here (pre-SigV4) so Bedrock-backed
		// Claude traffic isn't hard-failed by a header the upstream ignores.
		if isAnthropicMantlePath(req.URL.Path) {
			req.Header.Del("Anthropic-Beta")
		}
		// Scope the request to a project before signing so Mantle applies that
		// project's data-retention policy. Set here (pre-SigV4) so the header is
		// covered by the signature the upstream validates.
		mantle.applyProjectHeader(req)
		req.Header.Del("Authorization")
		req.Header.Del("X-Amz-Date")
		req.Header.Del("X-Amz-Content-Sha256")
		req.Header.Del("X-Amz-Security-Token")
		// AWS's edge appends the client address to X-Forwarded-For. Do not
		// include a mutable forwarding header in the SigV4 canonical request.
		req.Header.Del("X-Forwarded-For")
		if opt.DisableGzip {
			req.Header.Del("Accept-Encoding")
		}
		log.Printf("Proxying Bedrock Mantle request: %s %s", req.Method, req.URL.Path)
	}
	proxy.Transport = &sigV4Transport{
		credentials:   credentials,
		inner:         newProxyTransport(opt.DisableGzip),
		region:        region,
		regionForPath: mantle.regionForPath,
		service:       bedrockMantleService,
	}
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
		proxylog.Upstream("bedrock-mantle reverse proxy transport error: %v", err)
		if mantle.IsStreamingRequest(r) {
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set(proxylog.HeaderErrorSource, proxylog.ErrorSourceUpstream)
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, proxylog.UpstreamSSEDataLine("bedrock-mantle transport: %v", err))
				fmt.Fprintf(w, "data: [DONE]\n\n")
			}
			return
		}
		proxylog.UpstreamHTTPError(w, fmt.Sprintf("bedrock-mantle transport: %v", err), http.StatusBadGateway)
	}
	return mantle
}

// isAnthropicMantlePath reports whether a (prefix-trimmed) Mantle request path
// targets the Anthropic Messages API rather than the OpenAI-compatible API.
func isAnthropicMantlePath(path string) bool {
	return strings.Contains(path, "/anthropic/")
}

// regionForPath returns the AWS region to sign a Mantle request for, routing
// Anthropic Messages traffic to anthropicRegion and everything else to region.
func (b *BedrockMantleProxy) regionForPath(path string) string {
	if isAnthropicMantlePath(path) {
		return b.anthropicRegion
	}
	return b.region
}

// hostForPath returns the upstream host for a Mantle request when it differs
// from the default-region host, or "" when the default host should be kept.
func (b *BedrockMantleProxy) hostForPath(path string) string {
	region := b.regionForPath(path)
	if region == b.region {
		return ""
	}
	return fmt.Sprintf("bedrock-mantle.%s.api.aws", region)
}

// sigV4Transport signs the final URL, headers, and body immediately before
// handing the request to the underlying transport.
type sigV4Transport struct {
	credentials aws.CredentialsProvider
	inner       http.RoundTripper
	region      string
	// regionForPath, when set, resolves the signing region per request path so
	// Anthropic and OpenAI Mantle traffic can each sign for its own AWS region.
	regionForPath func(path string) string
	service       string
}

func (t *sigV4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// ReverseProxy adds this after Director. AWS may append it at its edge,
	// invalidating any signature that includes the header.
	req.Header.Del("X-Forwarded-For")
	payload, err := readAndRestoreMantleBody(req)
	if err != nil {
		return nil, fmt.Errorf("read Bedrock Mantle request body: %w", err)
	}
	sanitized := stripMantleUnsupportedToolStrict(payload)
	if !bytes.Equal(sanitized, payload) {
		payload = sanitized
		restoreMantleRequestBody(req, payload)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	credentials, err := t.credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials for Bedrock Mantle: %w", err)
	}
	region := t.region
	if t.regionForPath != nil {
		region = t.regionForPath(req.URL.Path)
	}
	if err := v4.NewSigner().SignHTTP(req.Context(), credentials, req, payloadHash, t.service, region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign Bedrock Mantle request: %w", err)
	}
	return t.inner.RoundTrip(req)
}

func stripMantleUnsupportedToolStrict(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(payload, &root) != nil {
		return payload
	}
	toolsRaw, ok := root["tools"]
	if !ok {
		return payload
	}
	var tools []map[string]any
	if json.Unmarshal(toolsRaw, &tools) != nil {
		return payload
	}
	changed := false
	for i := range tools {
		if _, ok := tools[i]["strict"]; ok {
			delete(tools[i], "strict")
			changed = true
		}
		if fn, ok := tools[i]["function"].(map[string]any); ok {
			if _, ok := fn["strict"]; ok {
				delete(fn, "strict")
				changed = true
			}
		}
		if custom, ok := tools[i]["custom"].(map[string]any); ok {
			if _, ok := custom["strict"]; ok {
				delete(custom, "strict")
				changed = true
			}
		}
	}
	if !changed {
		return payload
	}
	newTools, err := json.Marshal(tools)
	if err != nil {
		return payload
	}
	root["tools"] = newTools
	out, err := json.Marshal(root)
	if err != nil {
		return payload
	}
	return out
}

func restoreMantleRequestBody(req *http.Request, payload []byte) {
	req.Body = io.NopCloser(bytes.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	req.ContentLength = int64(len(payload))
}

func readAndRestoreMantleBody(req *http.Request) ([]byte, error) {
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

// openAIProjectHeader scopes a Mantle request to a Bedrock project. Mantle
// speaks the OpenAI wire protocol, so it reuses OpenAI's project header name.
const openAIProjectHeader = "OpenAI-Project"

// applyProjectHeader sets the OpenAI-Project header when the request's model is
// mapped to a project. It never overrides a project the caller already sent, and
// is a no-op when no mapping is configured. Must run before SigV4 signing so the
// header is covered by the signature.
func (b *BedrockMantleProxy) applyProjectHeader(req *http.Request) {
	if len(b.modelProjects) == 0 || req.Header.Get(openAIProjectHeader) != "" {
		return
	}
	model := b.modelFromRequest(req)
	if model == "" {
		return
	}
	if project := b.modelProjects[model]; project != "" {
		req.Header.Set(openAIProjectHeader, project)
	}
}

// modelFromRequest returns the model id from a POST body, restoring the body for
// downstream readers. Returns "" for non-POST requests or unparseable bodies.
func (b *BedrockMantleProxy) modelFromRequest(req *http.Request) string {
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return ""
	}
	body, err := readAndRestoreMantleBody(req)
	if err != nil {
		return ""
	}
	var data struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &data) != nil {
		return ""
	}
	return data.Model
}

func (b *BedrockMantleProxy) GetName() string { return bedrockMantleName }

func (b *BedrockMantleProxy) IsStreamingRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	if req.Method != http.MethodPost {
		return false
	}
	if strings.Contains(req.URL.Path, "/messages") ||
		strings.Contains(req.URL.Path, "/responses") ||
		strings.Contains(req.URL.Path, "/completions") {
		if strings.Contains(req.URL.Path, "/messages") {
			return (&AnthropicProxy{}).checkStreamingInBody(req)
		}
		return (&OpenAIProxy{}).checkStreamingInBody(req)
	}
	return false
}

func (b *BedrockMantleProxy) Proxy() http.Handler { return b.proxy }

func (b *BedrockMantleProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	b.proxy.Transport = fn(b.proxy.Transport)
}

func (b *BedrockMantleProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          bedrockMantleName,
		"status":            "healthy",
		"baseURL":           b.baseURL,
		"region":            b.region,
		"anthropic_region":  b.anthropicRegion,
		"streaming_support": true,
		"auth":              "proxy_api_key_and_task_sigv4",
	}
}

func (b *BedrockMantleProxy) UserIDFromRequest(req *http.Request) string {
	if req == nil || req.Method != http.MethodPost {
		return ""
	}
	body, err := readAndRestoreMantleBody(req)
	if err != nil {
		return ""
	}
	var payload struct {
		User string `json:"user"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	return payload.User
}

// ValidateAPIKey accepts llm-proxy keys registered for the Bedrock family.
// OpenAI clients send “Authorization: Bearer“; Anthropic Messages clients
// (langchain_anthropic / the Anthropic SDK) send “x-api-key“. Both are
// accepted and stripped before SigV4 signing — Mantle upstream auth is AWS
// credentials only.
func (b *BedrockMantleProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	key := mantleProxyKeyFromRequest(req)
	if key == "" {
		return fmt.Errorf("Bearer proxy API key is required")
	}
	_, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), key)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}
	if !apikeys.IsBedrockFamilyProvider(provider) {
		return fmt.Errorf("API key is for provider %s, not a Bedrock proxy key", provider)
	}
	mantleStripProxyAuthHeaders(req)
	return nil
}

func mantleProxyKeyFromRequest(req *http.Request) string {
	if auth := req.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return req.Header.Get("x-api-key")
}

func mantleStripProxyAuthHeaders(req *http.Request) {
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
}

func (b *BedrockMantleProxy) RegisterExtraRoutes(_ *mux.Router) {}

func (b *BedrockMantleProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	if req == nil || req.Body == nil || req.Method != http.MethodPost {
		return "", nil
	}
	body, err := readAndRestoreMantleBody(req)
	if err != nil {
		return "", nil
	}
	var data struct {
		Model    string          `json:"model"`
		Input    json.RawMessage `json:"input"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &data) != nil {
		return "", nil
	}
	var messages []string
	appendText := func(raw json.RawMessage) {
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			messages = append(messages, text)
			return
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			for _, part := range parts {
				if (part.Type == "text" || part.Type == "input_text") && part.Text != "" {
					messages = append(messages, part.Text)
				}
			}
		}
	}
	appendText(data.Input)
	for _, message := range data.Messages {
		appendText(message.Content)
	}
	return data.Model, messages
}

func (b *BedrockMantleProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	decompressedReader, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("decompress Bedrock Mantle response: %w", err)
	}
	if isStreaming {
		return parseMantleStream(decompressedReader)
	}
	body, err := io.ReadAll(decompressedReader)
	if err != nil {
		return nil, err
	}
	var responseType struct {
		Choices json.RawMessage `json:"choices"`
	}
	if json.Unmarshal(body, &responseType) == nil && responseType.Choices != nil {
		metadata, err := (&OpenAIProxy{}).ParseResponseMetadata(bytes.NewReader(body), false)
		if metadata != nil {
			metadata.Provider = bedrockMantleName
		}
		return metadata, err
	}
	return parseMantleMetadata(body, false)
}

func parseMantleStream(responseBody io.Reader) (*LLMResponseMetadata, error) {
	scanner := bufio.NewScanner(responseBody)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	var partial *LLMResponseMetadata
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "data: ")
		if line == scanner.Text() || line == "[DONE]" {
			continue
		}
		metadata, err := parseMantleMetadata([]byte(line), true)
		if err == nil {
			if metadata.TotalTokens > 0 {
				return metadata, nil
			}
			if metadata.Model != "" {
				partial = metadata
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if partial != nil {
		return partial, nil
	}
	return nil, fmt.Errorf("no usage information found in Bedrock Mantle stream")
}

func parseMantleMetadata(body []byte, streaming bool) (*LLMResponseMetadata, error) {
	var envelope struct {
		Model    string       `json:"model"`
		ID       string       `json:"id"`
		Status   string       `json:"status"`
		Type     string       `json:"type"`
		Usage    *mantleUsage `json:"usage"`
		Response *struct {
			Model  string       `json:"model"`
			ID     string       `json:"id"`
			Status string       `json:"status"`
			Usage  *mantleUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	usage := envelope.Usage
	if envelope.Response != nil {
		if envelope.Model == "" {
			envelope.Model = envelope.Response.Model
		}
		if envelope.ID == "" {
			envelope.ID = envelope.Response.ID
		}
		if envelope.Status == "" {
			envelope.Status = envelope.Response.Status
		}
		if usage == nil && envelope.Response.Usage != nil {
			usage = envelope.Response.Usage
		}
	}
	if usage == nil {
		return &LLMResponseMetadata{Model: envelope.Model, RequestID: envelope.ID, Provider: bedrockMantleName, IsStreaming: streaming}, nil
	}
	input, output := usage.InputTokens, usage.OutputTokens
	if input == 0 && output == 0 {
		input, output = usage.PromptTokens, usage.CompletionTokens
	}
	total := usage.TotalTokens
	if total == 0 {
		total = input + output
	}
	return &LLMResponseMetadata{
		Model: envelope.Model, RequestID: envelope.ID, Provider: bedrockMantleName,
		InputTokens: input, OutputTokens: output, TotalTokens: total,
		FinishReason: envelope.Status, IsStreaming: streaming,
		CacheReadInputTokens: usage.InputTokensDetails.CachedTokens,
		ThoughtTokens:        usage.OutputTokensDetails.ReasoningTokens,
	}, nil
}

type mantleUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	PromptTokens       int `json:"prompt_tokens"`
	CompletionTokens   int `json:"completion_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}
