package providers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
)

type mantleRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f mantleRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type mantleKeyStore struct{}

func (mantleKeyStore) ValidateAndGetActualKey(_ context.Context, key string) (string, string, error) {
	if key != "sk-iw-mantle" {
		return "", "", io.EOF
	}
	return "discarded-provider-token", bedrockMantleName, nil
}

func mustNewBedrockMantleProxy(t *testing.T, opts ...ProxyOptions) *BedrockMantleProxy {
	t.Helper()
	proxy, err := NewBedrockMantleProxy(opts...)
	if err != nil {
		t.Fatalf("NewBedrockMantleProxy: %v", err)
	}
	return proxy
}

func TestBedrockMantle_RewritesAnthropicMessagesRequest(t *testing.T) {
	body := []byte(`{"model":"anthropic.claude-haiku-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"anthropic.claude-haiku-4-5","usage":{"input_tokens":2,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", "sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.URL.String() != "https://bedrock-mantle.us-west-2.api.aws/anthropic/v1/messages" {
		t.Fatalf("upstream URL = %q", got.URL.String())
	}
}

func TestBedrockMantle_RoutesAnthropicToConfiguredRegion(t *testing.T) {
	body := []byte(`{"model":"anthropic.claude-haiku-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleAnthropicRegion: "us-east-1"},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"anthropic.claude-haiku-4-5","usage":{"input_tokens":2,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", "sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.URL.String() != "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages" {
		t.Fatalf("upstream URL = %q, want us-east-1 host", got.URL.String())
	}
	if auth := got.Header.Get("Authorization"); !strings.Contains(auth, "/us-east-1/bedrock-mantle/aws4_request") {
		t.Fatalf("Anthropic request not signed for us-east-1: %q", auth)
	}
}

func TestBedrockMantle_KeepsOpenAIInDefaultRegion(t *testing.T) {
	body := []byte(`{"model":"openai.gpt-5.4","input":"hello"}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleAnthropicRegion: "us-east-1"},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"openai.gpt-5.4","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.URL.String() != "https://bedrock-mantle.us-west-2.api.aws/openai/v1/responses" {
		t.Fatalf("upstream URL = %q, want us-west-2 host", got.URL.String())
	}
	if auth := got.Header.Get("Authorization"); !strings.Contains(auth, "/us-west-2/bedrock-mantle/aws4_request") {
		t.Fatalf("OpenAI request not signed for us-west-2: %q", auth)
	}
}

func TestBedrockMantle_StripsAnthropicBetaHeader(t *testing.T) {
	body := []byte(`{"model":"anthropic.claude-haiku-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleAnthropicRegion: "us-east-1"},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"anthropic.claude-haiku-4-5","usage":{"input_tokens":2,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", "sk-iw-mantle")
	req.Header.Set("Anthropic-Beta", "advanced-tool-use-2025-11-20")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.Header.Get("Anthropic-Beta") != "" {
		t.Fatalf("Anthropic-Beta must be stripped for Mantle, got %q", got.Header.Get("Anthropic-Beta"))
	}
}

func TestBedrockMantle_KeepsBetaHeaderOffOpenAIStrip(t *testing.T) {
	// OpenAI Mantle traffic never carries an Anthropic-Beta header, and the
	// strip must not touch the OpenAI path's own request shape.
	body := []byte(`{"model":"openai.gpt-5.4","input":"hello"}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"openai.gpt-5.4","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	req.Header.Set("Anthropic-Beta", "test-beta")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.URL.String() != "https://bedrock-mantle.us-west-2.api.aws/openai/v1/responses" {
		t.Fatalf("upstream URL = %q", got.URL.String())
	}
	if got.Header.Get("Anthropic-Beta") != "test-beta" {
		t.Fatalf("Anthropic-Beta unexpectedly stripped: %q", got.Header.Get("Anthropic-Beta"))
	}
}

func TestBedrockMantle_IsStreamingRequestForMessages(t *testing.T) {
	proxy := mustNewBedrockMantleProxy(t)
	body := []byte(`{"model":"anthropic.claude-haiku-4-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/anthropic/v1/messages", bytes.NewReader(body))
	if !proxy.IsStreamingRequest(req) {
		t.Fatal("expected streaming request for Anthropic messages with stream=true")
	}
}

func TestBedrockMantle_AcceptsAnthropicXApiKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("x-api-key", "sk-iw-mantle")
	if err := mustNewBedrockMantleProxy(t).ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	if req.Header.Get("x-api-key") != "" {
		t.Fatal("x-api-key should be stripped after validation")
	}
}

func TestBedrockMantle_AcceptsBedrockProviderKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-iw-bedrock")
	if err := mustNewBedrockMantleProxy(t).ValidateAPIKey(req, bedrockProviderKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
}

func TestBedrockMantle_TaskSigV4AuthAllowsMissingKey(t *testing.T) {
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleTaskSigV4Auth: true},
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey with empty key: %v", err)
	}
}

func TestBedrockMantle_TaskSigV4AuthAllowsPlaceholderKey(t *testing.T) {
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleTaskSigV4Auth: true},
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bedrock-sidecar")
	if err := proxy.ValidateAPIKey(req, passthroughKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey with placeholder: %v", err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("placeholder Authorization should be stripped before SigV4")
	}
}

func TestBedrockMantle_TaskSigV4AuthStillValidatesProxyKeys(t *testing.T) {
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleTaskSigV4Auth: true},
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey with Mantle proxy key: %v", err)
	}
}

func TestBedrockMantle_TaskSigV4AuthRejectsWrongProviderProxyKey(t *testing.T) {
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleTaskSigV4Auth: true},
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk-iw-openai")
	if err := proxy.ValidateAPIKey(req, openaiProxyKeyStore{}); err == nil {
		t.Fatal("expected wrong-provider iw-* key to fail")
	}
}

func TestBedrockMantle_HealthAuthReflectsTaskSigV4(t *testing.T) {
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleTaskSigV4Auth: true},
	)
	if got := proxy.GetHealthStatus()["auth"]; got != "task_sigv4" {
		t.Fatalf("auth=%v, want task_sigv4", got)
	}
}

type bedrockProviderKeyStore struct{}

func (bedrockProviderKeyStore) ValidateAndGetActualKey(_ context.Context, key string) (string, string, error) {
	if key != "sk-iw-bedrock" {
		return "", "", io.EOF
	}
	return "unused", "bedrock", nil
}

type passthroughKeyStore struct{}

func (passthroughKeyStore) ValidateAndGetActualKey(_ context.Context, key string) (string, string, error) {
	return key, "", nil
}

type openaiProxyKeyStore struct{}

func (openaiProxyKeyStore) ValidateAndGetActualKey(_ context.Context, key string) (string, string, error) {
	if key != "sk-iw-openai" {
		return "", "", io.EOF
	}
	return "sk-openai", "openai", nil
}

func TestBedrockMantle_RewritesStripsAndSignsRequest(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	var gotBody []byte
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		gotBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"claude-sonnet-4-5","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	if got.URL.String() != "https://bedrock-mantle.us-west-2.api.aws/openai/v1/responses" {
		t.Fatalf("upstream URL = %q", got.URL.String())
	}
	if got.Header.Get("Authorization") == "Bearer sk-iw-mantle" {
		t.Fatal("inbound bearer token reached the upstream")
	}
	if !strings.Contains(got.Header.Get("Authorization"), "AWS4-HMAC-SHA256") ||
		!strings.Contains(got.Header.Get("Authorization"), "/bedrock-mantle/aws4_request") {
		t.Fatalf("request was not signed for Bedrock Mantle: %q", got.Header.Get("Authorization"))
	}
	if got.Header.Get("X-Amz-Security-Token") != "session" {
		t.Fatalf("session token = %q, want signer credential token", got.Header.Get("X-Amz-Security-Token"))
	}
	if got.Header.Get("X-Forwarded-For") != "" {
		t.Fatalf("X-Forwarded-For must not be signed: %q", got.Header.Get("X-Forwarded-For"))
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body changed: got %q want %q", gotBody, body)
	}
}

func TestBedrockMantle_StripsCloudflareAndClientHopHeadersBeforeSigning(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Headers Cloudflare / OpenAI SDKs inject in front of llm.instawork.com.
	req.Header.Set("CDN-Loop", "cloudflare; loops=1")
	req.Header.Set("CF-Connecting-IP", "73.143.211.71")
	req.Header.Set("CF-IPCountry", "US")
	req.Header.Set("CF-Ray", "a1c2110829e6aff6-SEA")
	req.Header.Set("CF-Visitor", `{"scheme":"https"}`)
	req.Header.Set("Cookie", "__cf_bm=example")
	req.Header.Set("X-Bot-Score", "1")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Forwarded-Port", "3600")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Stainless-Lang", "python")
	req.Header.Set("X-Stainless-Package-Version", "2.37.0")
	req.Header.Set("X-Stainless-Runtime", "CPython")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("upstream transport was not called")
	}

	forbidden := []string{
		"CDN-Loop", "CF-Connecting-IP", "CF-IPCountry", "CF-Ray", "CF-Visitor",
		"Cookie", "X-Bot-Score", "X-Forwarded-For", "X-Forwarded-Port",
		"X-Forwarded-Proto", "X-Stainless-Lang", "X-Stainless-Package-Version",
		"X-Stainless-Runtime",
	}
	for _, name := range forbidden {
		if got.Header.Get(name) != "" {
			t.Fatalf("%s must be stripped before upstream, got %q", name, got.Header.Get(name))
		}
	}
	auth := strings.ToLower(got.Header.Get("Authorization"))
	for _, needle := range []string{
		"cdn-loop", "cf-ray", "cf-connecting-ip", "cookie", "x-bot-score",
		"x-forwarded-for", "x-forwarded-port", "x-forwarded-proto", "x-stainless-",
	} {
		if strings.Contains(auth, needle) {
			t.Fatalf("SignedHeaders must not include %q: %s", needle, got.Header.Get("Authorization"))
		}
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type should be preserved, got %q", got.Header.Get("Content-Type"))
	}
	if !strings.Contains(got.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		t.Fatalf("request was not SigV4-signed: %q", got.Header.Get("Authorization"))
	}
}

func mantleUpstreamHeader(t *testing.T, opt ProxyOptions, model, callerProject string) http.Header {
	t.Helper()
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		opt,
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var got *http.Request
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"` + model + `","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)),
			Request:    req,
		}, nil
	})

	body := []byte(`{"model":"` + model + `","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	if callerProject != "" {
		req.Header.Set("OpenAI-Project", callerProject)
	}
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)
	if got == nil {
		t.Fatal("upstream transport was not called")
	}
	return got.Header
}

func TestBedrockMantle_InjectsProjectHeaderForMappedModel(t *testing.T) {
	opt := ProxyOptions{MantleModelProjects: map[string]string{"openai.gpt-5.5": "proj_abc123"}}
	h := mantleUpstreamHeader(t, opt, "openai.gpt-5.5", "")
	if got := h.Get("OpenAI-Project"); got != "proj_abc123" {
		t.Fatalf("OpenAI-Project = %q, want proj_abc123", got)
	}
	// The header must be inside the SigV4 SignedHeaders list, else the upstream
	// rejects the signature.
	if auth := h.Get("Authorization"); !strings.Contains(auth, "openai-project") {
		t.Fatalf("OpenAI-Project not covered by signature: %q", auth)
	}
}

func TestBedrockMantle_NoProjectHeaderForUnmappedModel(t *testing.T) {
	opt := ProxyOptions{MantleModelProjects: map[string]string{"openai.gpt-5.5": "proj_abc123"}}
	h := mantleUpstreamHeader(t, opt, "anthropic.claude-sonnet-5", "")
	if got := h.Get("OpenAI-Project"); got != "" {
		t.Fatalf("OpenAI-Project = %q, want empty for unmapped model", got)
	}
}

func TestBedrockMantle_PreservesCallerProjectHeader(t *testing.T) {
	opt := ProxyOptions{MantleModelProjects: map[string]string{"openai.gpt-5.5": "proj_abc123"}}
	h := mantleUpstreamHeader(t, opt, "openai.gpt-5.5", "proj_caller")
	if got := h.Get("OpenAI-Project"); got != "proj_caller" {
		t.Fatalf("OpenAI-Project = %q, want caller value proj_caller", got)
	}
}

func TestBedrockMantle_ParseResponsesMetadataIncludesCostFields(t *testing.T) {
	body := `{
		"id":"resp_1",
		"model":"openai.gpt-5.4",
		"status":"completed",
		"usage":{
			"input_tokens":12,
			"output_tokens":4,
			"total_tokens":16,
			"input_tokens_details":{"cached_tokens":3},
			"output_tokens_details":{"reasoning_tokens":2}
		}
	}`
	metadata, err := mustNewBedrockMantleProxy(t).ParseResponseMetadata(strings.NewReader(body), false)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if metadata.Provider != bedrockMantleName || metadata.Model != "openai.gpt-5.4" || metadata.TotalTokens != 16 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.CacheReadInputTokens != 3 || metadata.ThoughtTokens != 2 {
		t.Fatalf("missing Mantle cost fields: %+v", metadata)
	}
}

func TestBedrockMantle_ParseChatCompletionsMetadata(t *testing.T) {
	body := `{
		"id":"chatcmpl_1",
		"model":"openai.gpt-5.4",
		"choices":[{"finish_reason":"stop"}],
		"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}
	}`
	metadata, err := mustNewBedrockMantleProxy(t).ParseResponseMetadata(strings.NewReader(body), false)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if metadata.Provider != bedrockMantleName || metadata.InputTokens != 12 || metadata.OutputTokens != 4 || metadata.FinishReason != "stop" {
		t.Fatalf("unexpected chat completions metadata: %+v", metadata)
	}
}

func TestBedrockMantle_ParseResponsesStreamingUsage(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_1","model":"claude-sonnet-4-5"}}

data: {"type":"response.done","response":{"id":"resp_1","model":"claude-sonnet-4-5","status":"completed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}

data: [DONE]
`
	metadata, err := mustNewBedrockMantleProxy(t).ParseResponseMetadata(strings.NewReader(stream), true)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if metadata.Provider != bedrockMantleName || metadata.Model != "claude-sonnet-4-5" || metadata.TotalTokens != 16 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.CacheReadInputTokens != 3 || metadata.ThoughtTokens != 2 {
		t.Fatalf("missing Mantle streaming cost fields: %+v", metadata)
	}
}

func TestBedrockMantle_ParseAnthropicStreamingUsage(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"anthropic.claude-haiku-4-5","stop_reason":null,"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}

event: message_stop
data: {"type":"message_stop"}

`
	metadata, err := mustNewBedrockMantleProxy(t).ParseResponseMetadata(strings.NewReader(stream), true)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if metadata.Provider != bedrockMantleName || metadata.Model != "anthropic.claude-haiku-4-5" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.InputTokens != 25 || metadata.OutputTokens != 16 || metadata.TotalTokens != 41 {
		t.Fatalf("unexpected token aggregation: %+v", metadata)
	}
	if metadata.CacheReadInputTokens != 5 || metadata.CacheCreationInputTokens != 2 {
		t.Fatalf("missing Anthropic cache fields: %+v", metadata)
	}
	if metadata.FinishReason != "end_turn" {
		t.Fatalf("FinishReason = %q, want end_turn", metadata.FinishReason)
	}
}

func TestStripMantleUnsupportedToolStrict(t *testing.T) {
	in := []byte(`{"model":"anthropic.claude-haiku-4-5","tools":[{"name":"a","strict":true},{"type":"function","function":{"name":"b","strict":true}},{"type":"custom","custom":{"strict":true}}]}`)
	out := stripMantleUnsupportedToolStrict(in)
	if bytes.Contains(out, []byte(`"strict"`)) {
		t.Fatalf("expected strict stripped, got %s", out)
	}
}

func TestStripMantleUnsupportedToolStrict_PreservesLargeIntegers(t *testing.T) {
	in := []byte(`{"model":"anthropic.claude-haiku-4-5","tools":[{"name":"a","strict":true,"input_schema":{"type":"object","properties":{"n":{"const":9007199254740993}}}}]}`)
	out := stripMantleUnsupportedToolStrict(in)
	if bytes.Contains(out, []byte(`"strict"`)) {
		t.Fatalf("expected strict stripped, got %s", out)
	}
	if !bytes.Contains(out, []byte("9007199254740993")) {
		t.Fatalf("large integer mutated/lost: %s", out)
	}
}

func TestBedrockMantle_ExtractNestedResponsesInput(t *testing.T) {
	body := []byte(`{"model":"openai.gpt-5.4","input":[{"role":"user","content":[{"type":"input_text","text":"nested hello"}]}]}`)
	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/openai/v1/responses", bytes.NewReader(body))
	model, messages := (&BedrockMantleProxy{}).ExtractRequestModelAndMessages(req)
	if model != "openai.gpt-5.4" {
		t.Fatalf("model = %q", model)
	}
	if len(messages) != 1 || messages[0] != "nested hello" {
		t.Fatalf("messages = %#v, want [nested hello]", messages)
	}
}

func TestBedrockMantle_StripsStrictBeforeSigning(t *testing.T) {
	body := []byte(`{"model":"anthropic.claude-haiku-4-5","max_tokens":1024,"tools":[{"name":"strict-test","description":"d","input_schema":{"type":"object"},"strict":true}],"messages":[{"role":"user","content":"hello"}]}`)
	proxy := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	signerTransport := proxy.proxy.Transport.(*sigV4Transport)
	var gotBody []byte
	signerTransport.inner = mantleRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		gotBody, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"model":"anthropic.claude-haiku-4-5","usage":{"input_tokens":2,"output_tokens":1}}`)),
			Request:    req,
		}, nil
	})

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/anthropic/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", "sk-iw-mantle")
	if err := proxy.ValidateAPIKey(req, mantleKeyStore{}); err != nil {
		t.Fatalf("ValidateAPIKey: %v", err)
	}
	proxy.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	if bytes.Contains(gotBody, []byte(`"strict"`)) {
		t.Fatalf("upstream body still contains strict: %s", gotBody)
	}
}
