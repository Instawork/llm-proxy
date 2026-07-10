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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/gorilla/mux"
)

const (
	bedrockMantleName    = "bedrock-mantle"
	bedrockMantleService = "bedrock-mantle"
)

// BedrockMantleProxy forwards OpenAI-compatible requests to AWS Bedrock Mantle.
// It authenticates callers with llm-proxy API keys, then signs the rewritten
// upstream request with the task's AWS credential chain.
type BedrockMantleProxy struct {
	proxy   *httputil.ReverseProxy
	region  string
	baseURL string
}

// NewBedrockMantleProxy creates a Bedrock Mantle proxy using the default AWS
// credential chain. Credentials are resolved by the signer at request time.
func NewBedrockMantleProxy(opts ...ProxyOptions) *BedrockMantleProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = defaultBedrockRegion
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		panic(fmt.Sprintf("load AWS config for Bedrock Mantle: %v", err))
	}
	return newBedrockMantleProxy(region, cfg.Credentials, opt)
}

func newBedrockMantleProxy(region string, credentials aws.CredentialsProvider, opt ProxyOptions) *BedrockMantleProxy {
	baseURL := fmt.Sprintf("https://bedrock-mantle.%s.api.aws/openai", region)
	targetURL, err := url.Parse(baseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid Bedrock Mantle upstream URL %q: %v", baseURL, err))
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	mantle := &BedrockMantleProxy{proxy: proxy, region: region, baseURL: baseURL}
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/"+bedrockMantleName)
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, "/"+bedrockMantleName)
		}
		originalDirector(req)
		req.Host = targetURL.Host
		req.Header.Del("Authorization")
		req.Header.Del("X-Amz-Date")
		req.Header.Del("X-Amz-Content-Sha256")
		req.Header.Del("X-Amz-Security-Token")
		if opt.DisableGzip {
			req.Header.Del("Accept-Encoding")
		}
		log.Printf("Proxying Bedrock Mantle request: %s %s", req.Method, req.URL.Path)
	}
	proxy.Transport = &sigV4Transport{
		credentials: credentials,
		inner:       newProxyTransport(opt.DisableGzip),
		region:      region,
		service:     bedrockMantleService,
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
	return mantle
}

// sigV4Transport signs the final URL, headers, and body immediately before
// handing the request to the underlying transport.
type sigV4Transport struct {
	credentials aws.CredentialsProvider
	inner       http.RoundTripper
	region      string
	service     string
}

func (t *sigV4Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	payload, err := readAndRestoreMantleBody(req)
	if err != nil {
		return nil, fmt.Errorf("read Bedrock Mantle request body: %w", err)
	}
	sum := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	credentials, err := t.credentials.Retrieve(req.Context())
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials for Bedrock Mantle: %w", err)
	}
	if err := v4.NewSigner().SignHTTP(req.Context(), credentials, req, payloadHash, t.service, t.region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign Bedrock Mantle request: %w", err)
	}
	return t.inner.RoundTrip(req)
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

func (b *BedrockMantleProxy) GetName() string { return bedrockMantleName }

func (b *BedrockMantleProxy) IsStreamingRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	if req.Method != http.MethodPost || (!strings.Contains(req.URL.Path, "/responses") && !strings.Contains(req.URL.Path, "/completions")) {
		return false
	}
	body, err := readAndRestoreMantleBody(req)
	return err == nil && bytes.Contains(body, []byte(`"stream":true`))
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
		"streaming_support": true,
		"auth":              "proxy_api_key_and_task_sigv4",
	}
}

func (b *BedrockMantleProxy) UserIDFromRequest(_ *http.Request) string { return "" }

// ValidateAPIKey accepts only normal llm-proxy keys registered for Mantle.
// The resolved provider credential is deliberately discarded: Mantle is
// authenticated solely by the local AWS SigV4 signature.
func (b *BedrockMantleProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return fmt.Errorf("Bearer proxy API key is required")
	}
	key := strings.TrimPrefix(auth, "Bearer ")
	if key == "" {
		return fmt.Errorf("Bearer proxy API key is required")
	}
	_, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), key)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}
	if provider != bedrockMantleName {
		return fmt.Errorf("API key is for provider %s, not %s", provider, bedrockMantleName)
	}
	req.Header.Del("Authorization")
	return nil
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
	if isStreaming {
		return parseMantleStream(responseBody)
	}
	body, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, err
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
		Model  string `json:"model"`
		ID     string `json:"id"`
		Status string `json:"status"`
		Type   string `json:"type"`
		Usage  *struct {
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Response *struct {
			Model  string `json:"model"`
			ID     string `json:"id"`
			Status string `json:"status"`
			Usage  *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
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
			usage = &struct {
				InputTokens      int `json:"input_tokens"`
				OutputTokens     int `json:"output_tokens"`
				TotalTokens      int `json:"total_tokens"`
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			}{
				InputTokens:  envelope.Response.Usage.InputTokens,
				OutputTokens: envelope.Response.Usage.OutputTokens,
				TotalTokens:  envelope.Response.Usage.TotalTokens,
			}
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
	}, nil
}
