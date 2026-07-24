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
	// taskSigV4Auth allows missing / non-iw caller credentials on trusted
	// sidecars (providers.bedrock-mantle.auth=task_sigv4). Upstream auth is
	// still SigV4 from the task role.
	taskSigV4Auth bool
}

// NewBedrockMantleProxy creates a Bedrock Mantle proxy using the AWS default
// credential chain (env, shared config, ECS/EC2 task role). When
// BEDROCK_AWS_PROFILE or AWS_PROFILE is set, that shared config profile is
// used instead — docker-compose sets BEDROCK_AWS_PROFILE so DynamoDB Local
// static keys (AWS_ACCESS_KEY_ID=local) do not override real SigV4 creds.
// AWS config load failures are returned so callers can disable Mantle without
// taking down other providers.
func NewBedrockMantleProxy(opts ...ProxyOptions) (*BedrockMantleProxy, error) {
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
		return nil, fmt.Errorf("load AWS config for Bedrock Mantle: %w", err)
	}
	return newBedrockMantleProxy(region, cfg.Credentials, opt), nil
}

func loadBedrockMantleAWSConfig(ctx context.Context, region string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	// Only pin a shared-config profile when one is explicitly requested.
	// Forcing "default" breaks CI and ECS task-role auth when ~/.aws/config
	// has no such profile (AWS SDK error: failed to get shared config profile).
	profile := os.Getenv("BEDROCK_AWS_PROFILE")
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	return awsconfig.LoadDefaultConfig(ctx, opts...)
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
	mantle := &BedrockMantleProxy{
		proxy:           proxy,
		region:          region,
		baseURL:         baseURL,
		anthropicRegion: anthropicRegion,
		modelProjects:   opt.MantleModelProjects,
		taskSigV4Auth:   opt.MantleTaskSigV4Auth,
	}
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
		// Drop CDN/client hop headers before SigV4. Reverse proxies and OpenAI
		// SDKs inject headers that must not be forwarded or signed — otherwise
		// Mantle returns 401 InvalidSignature.
		stripMantleClientHopHeaders(req)
		if opt.DisableGzip {
			req.Header.Del("Accept-Encoding")
		}
		log.Printf("Proxying Bedrock Mantle request: %s %s", req.Method, req.URL.Path)
	}
	proxy.Transport = &sigV4Transport{
		credentials:   credentials,
		inner:         newProxyTransport(opt.DisableGzip, opt.ResponseHeaderTimeout),
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
		proxylog.WriteUpstreamJSONError(w, http.StatusBadGateway, fmt.Sprintf("bedrock-mantle transport: %v", err))
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
	// ReverseProxy may re-add X-Forwarded-For after Director; strip hop headers
	// again immediately before signing so they never enter the canonical request.
	stripMantleClientHopHeaders(req)
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
	var tools []json.RawMessage
	if json.Unmarshal(toolsRaw, &tools) != nil {
		return payload
	}
	changed := false
	for i := range tools {
		stripped, didChange := stripStrictFromToolObject(tools[i])
		if didChange {
			tools[i] = stripped
			changed = true
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

func stripStrictFromToolObject(toolRaw json.RawMessage) (json.RawMessage, bool) {
	var tool map[string]json.RawMessage
	if json.Unmarshal(toolRaw, &tool) != nil {
		return toolRaw, false
	}
	changed := false
	if _, ok := tool["strict"]; ok {
		delete(tool, "strict")
		changed = true
	}
	for _, nestKey := range []string{"function", "custom"} {
		nestedRaw, ok := tool[nestKey]
		if !ok {
			continue
		}
		var nested map[string]json.RawMessage
		if json.Unmarshal(nestedRaw, &nested) != nil {
			continue
		}
		if _, ok := nested["strict"]; !ok {
			continue
		}
		delete(nested, "strict")
		newNested, err := json.Marshal(nested)
		if err != nil {
			continue
		}
		tool[nestKey] = newNested
		changed = true
	}
	if !changed {
		return toolRaw, false
	}
	out, err := json.Marshal(tool)
	if err != nil {
		return toolRaw, false
	}
	return out, true
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
		return requestBodyHasStreamTrue(req, bedrockMantleName)
	}
	return false
}

func (b *BedrockMantleProxy) Proxy() http.Handler { return b.proxy }

func (b *BedrockMantleProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	b.proxy.Transport = fn(b.proxy.Transport)
}

func (b *BedrockMantleProxy) GetHealthStatus() map[string]interface{} {
	auth := "proxy_api_key_and_task_sigv4"
	if b.taskSigV4Auth {
		auth = "task_sigv4"
	}
	return map[string]interface{}{
		"provider":          bedrockMantleName,
		"status":            "healthy",
		"baseURL":           b.baseURL,
		"region":            b.region,
		"anthropic_region":  b.anthropicRegion,
		"streaming_support": true,
		"auth":              auth,
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
//
// When taskSigV4Auth is set (sidecar profile), an empty credential or a
// non-iw placeholder is also accepted: the co-located app is trusted, and
// the proxy signs upstream with the task role.
func (b *BedrockMantleProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	key := mantleProxyKeyFromRequest(req)
	if key == "" {
		if b.taskSigV4Auth {
			return nil
		}
		return fmt.Errorf("bearer proxy API key is required")
	}
	_, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), key)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}
	if apikeys.IsBedrockFamilyProvider(provider) {
		mantleStripProxyAuthHeaders(req)
		return nil
	}
	// Sidecar placeholder / non-proxy credential: strip and proceed. Real iw-*
	// keys for the wrong provider still fail so misconfigured keys are loud.
	if b.taskSigV4Auth && !apikeys.HasKeyPrefix(key) {
		mantleStripProxyAuthHeaders(req)
		return nil
	}
	return fmt.Errorf("API key is for provider %s, not a Bedrock proxy key", provider)
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

// mantleClientHopHeaderNames are exact header names (canonical MIME keys) that
// must not be forwarded to Bedrock Mantle or included in SigV4 SignedHeaders.
var mantleClientHopHeaderNames = []string{
	"Cookie",
	"Set-Cookie",
	"Connection",
	"Keep-Alive",
	"Proxy-Connection",
	"Transfer-Encoding",
	"Upgrade",
	"Te",
	"Trailer",
	"Via",
	"CDN-Loop",
	"Priority",
	"True-Client-IP",
	"X-Forwarded-For",
	"X-Forwarded-Port",
	"X-Forwarded-Proto",
	"X-Forwarded-Host",
	"X-Real-IP",
	"X-Bot-Score",
}

// stripMantleClientHopHeaders removes Cloudflare, forwarding, and SDK telemetry
// headers so the AWS SigV4 signer only covers Mantle-relevant headers.
func stripMantleClientHopHeaders(req *http.Request) {
	if req == nil {
		return
	}
	for _, name := range mantleClientHopHeaderNames {
		req.Header.Del(name)
	}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "cf-") || strings.HasPrefix(lower, "x-stainless-") {
			req.Header.Del(name)
		}
	}
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
		appendMantleContentText(raw, &messages)
	}
	appendText(data.Input)
	for _, message := range data.Messages {
		appendText(message.Content)
	}
	return data.Model, messages
}

// appendMantleContentText walks OpenAI Responses / Chat / Anthropic content
// shapes: a bare string, a flat text-part array, or nested message objects
// whose text lives under content / input[].content.
func appendMantleContentText(raw json.RawMessage, messages *[]string) {
	if len(raw) == 0 || string(raw) == "null" {
		return
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text != "" {
			*messages = append(*messages, text)
		}
		return
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		for _, part := range parts {
			appendMantleContentText(part, messages)
		}
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	if t, ok := obj["type"]; ok {
		var typeName string
		if json.Unmarshal(t, &typeName) == nil && (typeName == "text" || typeName == "input_text") {
			if textRaw, ok := obj["text"]; ok {
				var partText string
				if json.Unmarshal(textRaw, &partText) == nil && partText != "" {
					*messages = append(*messages, partText)
				}
			}
			return
		}
	}
	if content, ok := obj["content"]; ok {
		appendMantleContentText(content, messages)
	}
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
		return parseOpenAIFormatMetadata(bytes.NewReader(body), false, bedrockMantleName)
	}
	return parseMantleMetadata(body, false)
}

func parseMantleStream(responseBody io.Reader) (*LLMResponseMetadata, error) {
	data, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, err
	}
	if mantleStreamLooksAnthropic(data) {
		return parseAnthropicFormatMetadata(bytes.NewReader(data), true, bedrockMantleName)
	}
	return parseMantleOpenAIStream(bytes.NewReader(data))
}

func mantleStreamLooksAnthropic(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var tip struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(payload), &tip) != nil {
			continue
		}
		switch tip.Type {
		case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop":
			return true
		case "response.created", "response.completed", "response.done", "response.failed", "response.in_progress":
			return false
		}
	}
	return false
}

func parseMantleOpenAIStream(responseBody io.Reader) (*LLMResponseMetadata, error) {
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
		Model  string `json:"model"`
		ID     string `json:"id"`
		Status string `json:"status"`
		// StopReason is the Anthropic-native finish field ("stop_reason");
		// non-streaming /anthropic/v1/messages bodies land here (they have
		// no "choices" so they take this generic path, not the Anthropic
		// stream parser).
		StopReason string       `json:"stop_reason"`
		Type       string       `json:"type"`
		Usage      *mantleUsage `json:"usage"`
		Response   *struct {
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
	finish := envelope.Status
	if finish == "" {
		finish = envelope.StopReason
	}
	cacheRead := usage.InputTokensDetails.CachedTokens
	if cacheRead == 0 {
		cacheRead = usage.CacheReadInputTokens
	}
	return &LLMResponseMetadata{
		Model: envelope.Model, RequestID: envelope.ID, Provider: bedrockMantleName,
		InputTokens: input, OutputTokens: output, TotalTokens: total,
		FinishReason: finish, IsStreaming: streaming,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		ThoughtTokens:            usage.OutputTokensDetails.ReasoningTokens,
	}, nil
}

type mantleUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	TotalTokens      int `json:"total_tokens"`
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// Anthropic-native prompt-caching fields (non-streaming
	// /anthropic/v1/messages responses take this parser, and Anthropic
	// excludes cache reads from input_tokens — dropping these hid the
	// cached prompt portion from cost accounting entirely).
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	InputTokensDetails       struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}
