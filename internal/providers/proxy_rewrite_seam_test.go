package providers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin the contract at the httputil.ReverseProxy seam: the exact
// outbound request each provider hands to its transport after the proxy's
// Rewrite hook has run. They are driven through ServeHTTP so they exercise
// the real hook and the ReverseProxy plumbing around it (forwarding-header
// stripping, hop-by-hop handling), not a hand-rolled closure.

// seamRT captures the final outbound request as the upstream vendor would
// see it and answers with a canned 200 so ServeHTTP completes normally.
type seamRT struct {
	got  *http.Request
	body []byte
}

func (s *seamRT) RoundTrip(req *http.Request) (*http.Response, error) {
	s.got = req.Clone(req.Context())
	if req.Body != nil {
		s.body, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}, nil
}

type seamProvider interface {
	WrapTransport(func(http.RoundTripper) http.RoundTripper)
	Proxy() http.Handler
}

type seamCase struct {
	name       string
	prefix     string // URL prefix the router matches and the proxy must strip
	vendorHost string
	build      func(opt ProxyOptions) seamProvider
	reverse    func(seamProvider) *httputil.ReverseProxy
}

// httpSeamCases are the plain credential-passthrough providers wired through
// the shared generic rewrite hook.
func httpSeamCases() []seamCase {
	return []seamCase{
		{
			name: "openai", prefix: "/openai", vendorHost: "api.openai.com",
			build:   func(opt ProxyOptions) seamProvider { return NewOpenAIProxy(opt) },
			reverse: func(p seamProvider) *httputil.ReverseProxy { return p.(*OpenAIProxy).proxy },
		},
		{
			name: "anthropic", prefix: "/anthropic", vendorHost: "api.anthropic.com",
			build:   func(opt ProxyOptions) seamProvider { return NewAnthropicProxy(opt) },
			reverse: func(p seamProvider) *httputil.ReverseProxy { return p.(*AnthropicProxy).proxy },
		},
		{
			name: "gemini", prefix: "/gemini", vendorHost: "generativelanguage.googleapis.com",
			build:   func(opt ProxyOptions) seamProvider { return NewGeminiProxy(opt) },
			reverse: func(p seamProvider) *httputil.ReverseProxy { return p.(*GeminiProxy).proxy },
		},
	}
}

func bedrockSeamCase(t *testing.T) seamCase {
	t.Helper()
	t.Setenv("AWS_REGION", "us-west-2")
	return seamCase{
		name: "bedrock", prefix: "/bedrock", vendorHost: "bedrock-runtime.us-west-2.amazonaws.com",
		build:   func(opt ProxyOptions) seamProvider { return NewBedrockProxy(opt) },
		reverse: func(p seamProvider) *httputil.ReverseProxy { return p.(*BedrockProxy).proxy },
	}
}

// captureAtSeam installs a seamRT as the outermost transport and returns it.
func captureAtSeam(p seamProvider) *seamRT {
	rt := &seamRT{}
	p.WrapTransport(func(http.RoundTripper) http.RoundTripper { return rt })
	return rt
}

// newInboundWithForwardingHeaders builds a client request carrying every
// forwarding header a CDN or SDK might inject, plus a RemoteAddr (set by
// httptest.NewRequest) that a Director-mode ReverseProxy would append to
// X-Forwarded-For.
func newInboundWithForwardingHeaders(method, target string, body []byte) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "llm-proxy.example.internal")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "for=203.0.113.9;proto=https")
	return req
}

var forwardingHeaderNames = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"}

func assertNoForwardingHeaders(t *testing.T, got *http.Request) {
	t.Helper()
	for _, name := range forwardingHeaderNames {
		assert.Emptyf(t, got.Header.Values(name), "%s must not reach the upstream vendor", name)
	}
}

func TestProxySeam_ExactlyOneRewriteHookConfigured(t *testing.T) {
	cases := append(httpSeamCases(), bedrockSeamCase(t))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rp := tc.reverse(tc.build(ProxyOptions{}))
			// ReverseProxy.ServeHTTP responds 502 through the ErrorHandler when
			// both or neither hook is set; catch that misconfiguration directly.
			//lint:ignore SA1019 the deprecated Director field is exactly what this seam test guards against
			hasDirector := rp.Director != nil
			hasRewrite := rp.Rewrite != nil
			assert.NotEqual(t, hasDirector, hasRewrite,
				"exactly one of Director/Rewrite must be set (Director=%v Rewrite=%v)",
				hasDirector, hasRewrite)
		})
	}
	t.Run("bedrock-mantle", func(t *testing.T) {
		m := newBedrockMantleProxy("us-west-2",
			credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"), ProxyOptions{})
		//lint:ignore SA1019 the deprecated Director field is exactly what this seam test guards against
		assert.NotEqual(t, m.proxy.Director != nil, m.proxy.Rewrite != nil)
	})
}

func TestProxySeam_StripsPrefixPinsHostAndKeepsQuery(t *testing.T) {
	for _, tc := range httpSeamCases() {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build(ProxyOptions{})
			rt := captureAtSeam(p)
			body := []byte(`{"model":"m","stream":false}`)
			req := httptest.NewRequest(http.MethodPost, "http://localhost:9002"+tc.prefix+"/v1/things?alt=sse&key=abc", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer sk-client")
			rec := httptest.NewRecorder()
			p.Proxy().ServeHTTP(rec, req)

			require.NotNil(t, rt.got, "transport was not called")
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "https", rt.got.URL.Scheme)
			assert.Equal(t, tc.vendorHost, rt.got.URL.Host)
			assert.Equal(t, tc.vendorHost, rt.got.Host, "Host header must be pinned to the vendor host")
			assert.Equal(t, "/v1/things", rt.got.URL.Path, "provider prefix must be stripped exactly once")
			assert.Equal(t, "alt=sse&key=abc", rt.got.URL.RawQuery)
			assert.Equal(t, "Bearer sk-client", rt.got.Header.Get("Authorization"), "credentials pass through verbatim")
			assert.Equal(t, body, rt.body, "body must not be mutated")
		})
	}
}

func TestProxySeam_PrefixStrippedOnlyAtPathStart(t *testing.T) {
	// A path that merely contains the provider name deeper in must not be
	// altered; only the leading router prefix is removed.
	tc := httpSeamCases()[0]
	p := tc.build(ProxyOptions{})
	rt := captureAtSeam(p)
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9002/openai/v1/models/openai-model", nil)
	p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
	require.NotNil(t, rt.got)
	assert.Equal(t, "/v1/models/openai-model", rt.got.URL.Path)
}

func TestProxySeam_NoForwardingHeadersReachVendor(t *testing.T) {
	cases := append(httpSeamCases(), bedrockSeamCase(t))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.build(ProxyOptions{})
			rt := captureAtSeam(p)
			req := newInboundWithForwardingHeaders(http.MethodPost,
				"http://localhost:9002"+tc.prefix+"/v1/things", []byte(`{}`))
			p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
			require.NotNil(t, rt.got, "transport was not called")
			assertNoForwardingHeaders(t, rt.got)
		})
	}

	t.Run("bedrock-mantle", func(t *testing.T) {
		m := newBedrockMantleProxy("us-west-2",
			credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"), ProxyOptions{})
		// Capture after signing so we see exactly what AWS sees.
		rt := &seamRT{}
		m.proxy.Transport.(*sigV4Transport).inner = rt
		req := newInboundWithForwardingHeaders(http.MethodPost,
			"http://proxy/bedrock-mantle/openai/v1/responses", []byte(`{"model":"m","input":"hi"}`))
		req.Header.Set("Authorization", "Bearer sk-iw-mantle")
		require.NoError(t, m.ValidateAPIKey(req, mantleKeyStore{}))
		m.Proxy().ServeHTTP(httptest.NewRecorder(), req)
		require.NotNil(t, rt.got, "transport was not called")
		assertNoForwardingHeaders(t, rt.got)
		auth := strings.ToLower(rt.got.Header.Get("Authorization"))
		assert.Contains(t, auth, "aws4-hmac-sha256")
		assert.NotContains(t, auth, "x-forwarded-", "SignedHeaders must not cover forwarding headers")
		assert.NotContains(t, auth, "forwarded;", "SignedHeaders must not cover Forwarded")
	})
}

func TestProxySeam_AcceptEncoding(t *testing.T) {
	for _, tc := range httpSeamCases() {
		t.Run(tc.name+"/default keeps client Accept-Encoding", func(t *testing.T) {
			p := tc.build(ProxyOptions{})
			rt := captureAtSeam(p)
			req := httptest.NewRequest(http.MethodGet, "http://localhost:9002"+tc.prefix+"/v1/models", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
			require.NotNil(t, rt.got)
			assert.Equal(t, "gzip", rt.got.Header.Get("Accept-Encoding"))
		})
		t.Run(tc.name+"/DisableGzip strips Accept-Encoding", func(t *testing.T) {
			p := tc.build(ProxyOptions{DisableGzip: true})
			rt := captureAtSeam(p)
			req := httptest.NewRequest(http.MethodGet, "http://localhost:9002"+tc.prefix+"/v1/models", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
			require.NotNil(t, rt.got)
			assert.Empty(t, rt.got.Header.Values("Accept-Encoding"))
		})
	}
}

func TestProxySeam_Bedrock_PathRawPathHostAndSignedHeaders(t *testing.T) {
	tc := bedrockSeamCase(t)
	const signedAuth = "AWS4-HMAC-SHA256 Credential=AKID/20250101/us-west-2/bedrock/aws4_request, " +
		"SignedHeaders=accept-encoding;content-type;host;x-amz-date, Signature=deadbeef"

	t.Run("strips prefix from Path and RawPath, pins Host, passes body and every signed header through byte-for-byte", func(t *testing.T) {
		// Every header the client signed. If the proxy drops, renames, or
		// re-canonicalises any of these — or touches a single body byte — AWS
		// rejects the request with a signature mismatch, so the seam must
		// carry them verbatim.
		signedHeaders := map[string]string{
			"Content-Type":         "application/json; charset=utf-8",
			"X-Amz-Date":           "20250101T000000Z",
			"X-Amz-Content-Sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			"X-Amz-Security-Token": "FwoGZXIvYXdzEBYaDExampleSessionToken==",
			"X-Amz-Target":         "AmazonBedrockRuntime.Converse",
			"X-Amzn-Bedrock-Trace": "ENABLED",
		}
		fullAuth := "AWS4-HMAC-SHA256 Credential=AKID/20250101/us-west-2/bedrock/aws4_request, " +
			"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token;x-amz-target;x-amzn-bedrock-trace, " +
			"Signature=deadbeef"
		// Body with non-ASCII, escaped slashes, and whitespace the proxy must
		// not normalise (SigV4 hashes the exact bytes).
		body := []byte("{\"messages\":[{\"role\":\"user\",\"content\":[{\"text\":\"héllo \\/ wörld\\n\"}]}],  \"inferenceConfig\": {\"maxTokens\":  8}}")

		p := tc.build(ProxyOptions{})
		rt := captureAtSeam(p)
		req := httptest.NewRequest(http.MethodPost,
			"http://localhost:9002/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse", bytes.NewReader(body))
		req.Host = tc.vendorHost
		req.Header.Set("Authorization", fullAuth)
		for k, v := range signedHeaders {
			req.Header.Set(k, v)
		}
		p.Proxy().ServeHTTP(httptest.NewRecorder(), req)

		require.NotNil(t, rt.got)
		assert.Equal(t, "/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse", rt.got.URL.Path)
		assert.Equal(t, "/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse", rt.got.URL.EscapedPath(),
			"RawPath must be stripped too so the on-wire path matches the signed canonical path")
		assert.Equal(t, tc.vendorHost, rt.got.Host)
		assert.Equal(t, tc.vendorHost, rt.got.URL.Host)
		assert.Equal(t, fullAuth, rt.got.Header.Get("Authorization"), "SigV4 Authorization passes through verbatim")
		for k, v := range signedHeaders {
			assert.Equalf(t, []string{v}, rt.got.Header.Values(k), "signed header %s must pass through verbatim and exactly once", k)
		}
		assert.Equal(t, body, rt.body, "SigV4 body must reach the vendor byte-for-byte")
		assert.Equal(t, int64(len(body)), rt.got.ContentLength, "Content-Length must match the signed payload")
		assert.Empty(t, rt.got.Header.Values("Content-Encoding"), "proxy must not compress a signed body")
		assert.Empty(t, rt.got.Header.Values("Accept-Encoding"), "an unsigned Accept-Encoding must not be synthesised")
	})

	t.Run("plain path without escaped characters", func(t *testing.T) {
		p := tc.build(ProxyOptions{})
		rt := captureAtSeam(p)
		req := httptest.NewRequest(http.MethodPost, "http://localhost:9002/bedrock/model/x/converse", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", signedAuth)
		req.Header.Set("X-Amz-Date", "20250101T000000Z")
		p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
		require.NotNil(t, rt.got)
		assert.Equal(t, "/model/x/converse", rt.got.URL.Path)
		assert.Equal(t, signedAuth, rt.got.Header.Get("Authorization"))
		assert.Equal(t, "20250101T000000Z", rt.got.Header.Get("X-Amz-Date"))
	})

	t.Run("DisableGzip keeps Accept-Encoding when it is a signed header", func(t *testing.T) {
		p := tc.build(ProxyOptions{DisableGzip: true})
		rt := captureAtSeam(p)
		req := httptest.NewRequest(http.MethodPost, "http://localhost:9002/bedrock/model/x/converse", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", signedAuth)
		req.Header.Set("Accept-Encoding", "gzip")
		p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
		require.NotNil(t, rt.got)
		assert.Equal(t, "gzip", rt.got.Header.Get("Accept-Encoding"))
	})

	t.Run("DisableGzip strips Accept-Encoding when not signed", func(t *testing.T) {
		p := tc.build(ProxyOptions{DisableGzip: true})
		rt := captureAtSeam(p)
		req := httptest.NewRequest(http.MethodPost, "http://localhost:9002/bedrock/model/x/converse", bytes.NewReader([]byte(`{}`)))
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20250101/us-west-2/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=deadbeef")
		req.Header.Set("Accept-Encoding", "gzip")
		p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
		require.NotNil(t, rt.got)
		assert.Empty(t, rt.got.Header.Values("Accept-Encoding"))
	})
}

func TestProxySeam_BedrockMantle_RawPathAndRegionRetarget(t *testing.T) {
	m := newBedrockMantleProxy("us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{MantleAnthropicRegion: "us-east-1"})
	rt := &seamRT{}
	m.proxy.Transport.(*sigV4Transport).inner = rt

	req := httptest.NewRequest(http.MethodPost,
		"http://proxy/bedrock-mantle/anthropic/v1/messages/model%3Atag", bytes.NewReader([]byte(`{"model":"claude","messages":[]}`)))
	req.Header.Set("Authorization", "Bearer sk-iw-mantle")
	req.Header.Set("Anthropic-Beta", "tool-search-2025")
	require.NoError(t, m.ValidateAPIKey(req, mantleKeyStore{}))
	m.Proxy().ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, rt.got)
	assert.Equal(t, "/anthropic/v1/messages/model:tag", rt.got.URL.Path)
	assert.Equal(t, "/anthropic/v1/messages/model%3Atag", rt.got.URL.EscapedPath())
	assert.Equal(t, "bedrock-mantle.us-east-1.api.aws", rt.got.URL.Host, "Anthropic traffic retargets to the Anthropic region host")
	assert.Equal(t, "bedrock-mantle.us-east-1.api.aws", rt.got.Host)
	assert.Empty(t, rt.got.Header.Values("Anthropic-Beta"))
	assert.Contains(t, rt.got.Header.Get("Authorization"), "/us-east-1/bedrock-mantle/aws4_request", "signed for the retargeted region")
}
