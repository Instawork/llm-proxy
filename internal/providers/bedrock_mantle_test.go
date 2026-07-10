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

	req := httptest.NewRequest(http.MethodPost, "http://proxy/bedrock-mantle/v1/responses", bytes.NewReader(body))
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
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("body changed: got %q want %q", gotBody, body)
	}
}

func TestBedrockMantle_ParseResponsesStreamingUsage(t *testing.T) {
	stream := `data: {"type":"response.created","response":{"id":"resp_1","model":"claude-sonnet-4-5"}}

data: {"type":"response.done","response":{"id":"resp_1","model":"claude-sonnet-4-5","status":"completed","usage":{"input_tokens":12,"output_tokens":4,"total_tokens":16}}}

data: [DONE]
`
	metadata, err := NewBedrockMantleProxy().ParseResponseMetadata(strings.NewReader(stream), true)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if metadata.Provider != bedrockMantleName || metadata.Model != "claude-sonnet-4-5" || metadata.TotalTokens != 16 {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}
