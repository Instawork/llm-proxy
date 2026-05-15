package providers

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	eventstream "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// fakeBedrock spins up a httptest.Server pretending to be the upstream
// `bedrock-runtime.us-west-2.amazonaws.com` endpoint and lets the test
// inspect the inbound request after our proxy has run its director.
type fakeBedrockUpstream struct {
	srv          *httptest.Server
	gotMethod    string
	gotPath      string // decoded path (r.URL.Path)
	gotRawPath   string // raw path as received on the wire (EscapedPath)
	gotHost      string
	gotAuth      string
	gotAmzDate   string
	gotSHA256    string
	gotBody      []byte
	gotUserAgent string

	// Pluggable response: each test can write its own body / headers.
	respond func(w http.ResponseWriter, r *http.Request)
}

func newFakeBedrockUpstream(t *testing.T) *fakeBedrockUpstream {
	t.Helper()
	fb := &fakeBedrockUpstream{}
	fb.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb.gotMethod = r.Method
		fb.gotPath = r.URL.Path
		fb.gotRawPath = r.URL.EscapedPath()
		fb.gotHost = r.Host
		fb.gotAuth = r.Header.Get("Authorization")
		fb.gotAmzDate = r.Header.Get("X-Amz-Date")
		fb.gotSHA256 = r.Header.Get("X-Amz-Content-Sha256")
		fb.gotUserAgent = r.Header.Get("User-Agent")
		if r.Body != nil {
			fb.gotBody, _ = io.ReadAll(r.Body)
		}
		if fb.respond != nil {
			fb.respond(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fb.srv.Close)
	return fb
}

// wireProxyToFake replaces the proxy's transport so requests to the
// canonical AWS host are redirected to the fake upstream.  This lets us
// drive the real Director / ModifyResponse without actually hitting AWS.
func wireProxyToFake(b *BedrockProxy, fb *fakeBedrockUpstream) {
	target, _ := url.Parse(fb.srv.URL)
	b.proxy.Transport = &http.Transport{
		// Redirect every dial for the canonical host to the fake server.
		DialContext: (&http.Transport{}).DialContext,
		Proxy: func(req *http.Request) (*url.URL, error) {
			return nil, nil
		},
		// Replace via the request: rewrite URL in a RoundTripper wrapper instead.
	}
	b.proxy.Transport = &redirectTransport{target: target, inner: http.DefaultTransport}
}

// redirectTransport rewrites every outbound request's scheme + host to point at
// the fake httptest.Server. Path / headers / body are left untouched so we
// genuinely test what the proxy forwards.
type redirectTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return t.inner.RoundTrip(req)
}

// ---------------------------------------------------------------------------
// Passthrough fidelity tests
// ---------------------------------------------------------------------------

func TestBedrock_Passthrough_PreservesSignedHeaders(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	b := NewBedrockProxy()
	fb := newFakeBedrockUpstream(t)
	wireProxyToFake(b, fb)
	fb.respond = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"output":{"message":{"role":"assistant","content":[{"text":"ok"}]}},"stopReason":"end_turn","usage":{"inputTokens":7,"outputTokens":3,"totalTokens":10}}`))
	}

	body := []byte(`{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`)
	sum := sha256.Sum256(body)
	contentHash := hex.EncodeToString(sum[:])

	req := httptest.NewRequest(
		http.MethodPost,
		"http://localhost:9002/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		bytes.NewReader(body),
	)
	// These are the headers the boto3 SigV4 signer would have set against
	// the canonical Bedrock host — the proxy must hand them through verbatim.
	req.Host = "bedrock-runtime.us-west-2.amazonaws.com"
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIATEST/20260515/us-west-2/bedrock/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=deadbeef")
	req.Header.Set("X-Amz-Date", "20260515T123456Z")
	req.Header.Set("X-Amz-Content-Sha256", contentHash)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	b.Proxy().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	// URL prefix stripped
	if want := "/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse"; fb.gotPath != want {
		t.Errorf("upstream path: want %q, got %q", want, fb.gotPath)
	}
	// Host header pinned to canonical AWS hostname (passthrough invariant).
	if want := "bedrock-runtime.us-west-2.amazonaws.com"; fb.gotHost != want {
		t.Errorf("upstream Host: want %q, got %q", want, fb.gotHost)
	}
	// SigV4 headers preserved byte-for-byte.
	if !strings.HasPrefix(fb.gotAuth, "AWS4-HMAC-SHA256 Credential=AKIATEST/") {
		t.Errorf("Authorization header altered: %q", fb.gotAuth)
	}
	if fb.gotAmzDate != "20260515T123456Z" {
		t.Errorf("X-Amz-Date header altered: %q", fb.gotAmzDate)
	}
	if fb.gotSHA256 != contentHash {
		t.Errorf("X-Amz-Content-Sha256 altered: want %q got %q", contentHash, fb.gotSHA256)
	}
	// Body forwarded byte-identically.
	if !bytes.Equal(fb.gotBody, body) {
		t.Errorf("upstream body differs:\n  want %q\n   got %q", body, fb.gotBody)
	}
	// Recompute hash and confirm it still matches the signed digest.
	gotHash := sha256.Sum256(fb.gotBody)
	if hex.EncodeToString(gotHash[:]) != contentHash {
		t.Errorf("body bytes mutated through the proxy — content hash no longer matches signed value")
	}
}

func TestBedrock_StripsBedrockPrefix(t *testing.T) {
	cases := []struct {
		name              string
		in, wantOnTheWire string
	}{
		{"plain", "/bedrock/model/foo/converse", "/model/foo/converse"},
		{"streaming", "/bedrock/model/foo/converse-stream", "/model/foo/converse-stream"},
		// SigV4 invariant: model IDs containing `:` are URL-encoded to %3A
		// before signing, and the proxy must forward the encoded form
		// untouched so the upstream's canonical path matches the signature.
		{"encoded_colon", "/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse",
			"/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBedrockProxy()
			fb := newFakeBedrockUpstream(t)
			wireProxyToFake(b, fb)
			fb.respond = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

			req := httptest.NewRequest(http.MethodPost, "http://localhost:9002"+tc.in, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			b.Proxy().ServeHTTP(rec, req)

			if fb.gotRawPath != tc.wantOnTheWire {
				t.Errorf("on-wire path: want %q, got %q", tc.wantOnTheWire, fb.gotRawPath)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Streaming detection
// ---------------------------------------------------------------------------

func TestBedrock_IsStreamingRequest(t *testing.T) {
	b := NewBedrockProxy()
	cases := []struct {
		name, path, accept string
		want               bool
	}{
		{"converse-stream path", "/bedrock/model/foo/converse-stream", "", true},
		{"plain converse path", "/bedrock/model/foo/converse", "", false},
		{"accept eventstream", "/bedrock/model/foo/converse", bedrockEventStreamMIME, true},
		{"unknown path no accept", "/something-else", "", false},
		{"invoke with response stream", "/bedrock/model/foo/invoke-with-response-stream", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://localhost"+tc.path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if got := b.IsStreamingRequest(req); got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Model extraction
// ---------------------------------------------------------------------------

func TestExtractBedrockModelFromPath(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"/model/foo/converse", "foo"},
		{"/openai/v1/chat/completions", ""},
		{"/bedrock/", ""},
		{"/", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := ExtractBedrockModelFromPath(tc.in); got != tc.want {
				t.Errorf("path %q: want %q, got %q", tc.in, tc.want, got)
			}
		})
	}
}

func TestBedrock_ExtractRequestModelAndMessages(t *testing.T) {
	b := NewBedrockProxy()
	body := []byte(`{
		"messages":[
			{"role":"user","content":[{"text":"hello"},{"text":"world"}]},
			{"role":"assistant","content":[{"text":"hi"}]}
		],
		"system":[{"text":"system msg"}]
	}`)
	req := httptest.NewRequest(
		http.MethodPost,
		"http://localhost/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		bytes.NewReader(body),
	)
	req.Header.Set("Content-Type", "application/json")
	model, msgs := b.ExtractRequestModelAndMessages(req)
	if want := "us.anthropic.claude-sonnet-4-5-20250929-v1:0"; model != want {
		t.Errorf("model: want %q, got %q", want, model)
	}
	wantMsgs := []string{"hello", "world", "hi", "system msg"}
	if len(msgs) != len(wantMsgs) {
		t.Fatalf("msgs: want %d, got %d (%v)", len(wantMsgs), len(msgs), msgs)
	}
	for i, want := range wantMsgs {
		if msgs[i] != want {
			t.Errorf("msgs[%d]: want %q, got %q", i, want, msgs[i])
		}
	}
	// After extraction req.Body must yield the same bytes (SigV4 invariant).
	rest, _ := io.ReadAll(req.Body)
	if !bytes.Equal(rest, body) {
		t.Errorf("req.Body bytes mutated after extraction — SigV4 hash would be invalidated")
	}
}

// ---------------------------------------------------------------------------
// Response metadata parsing
// ---------------------------------------------------------------------------

func TestBedrock_ParseNonStreamingResponse(t *testing.T) {
	b := NewBedrockProxy()
	body := `{
		"output":{"message":{"role":"assistant","content":[{"text":"hi"}]}},
		"stopReason":"end_turn",
		"usage":{"inputTokens":42,"outputTokens":7,"totalTokens":49,"cacheReadInputTokens":12}
	}`
	md, err := b.ParseResponseMetadata(strings.NewReader(body), false)
	if err != nil {
		t.Fatalf("ParseResponseMetadata: %v", err)
	}
	if md.Provider != "bedrock" {
		t.Errorf("provider: %q", md.Provider)
	}
	if md.InputTokens != 42 || md.OutputTokens != 7 || md.TotalTokens != 49 {
		t.Errorf("usage: in=%d out=%d total=%d", md.InputTokens, md.OutputTokens, md.TotalTokens)
	}
	if md.CacheReadInputTokens != 12 {
		t.Errorf("cache_read: %d", md.CacheReadInputTokens)
	}
	if md.FinishReason != "end_turn" {
		t.Errorf("finish: %q", md.FinishReason)
	}
	if md.IsStreaming {
		t.Error("IsStreaming should be false for non-streaming")
	}
}

func TestBedrock_ParseNonStreamingResponse_Gzip(t *testing.T) {
	b := NewBedrockProxy()
	body := `{"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}}`
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(body))
	_ = gz.Close()

	md, err := b.ParseResponseMetadata(&buf, false)
	if err != nil {
		t.Fatalf("ParseResponseMetadata gzip: %v", err)
	}
	if md.InputTokens != 1 || md.OutputTokens != 2 || md.TotalTokens != 3 {
		t.Errorf("usage from gzip: in=%d out=%d total=%d", md.InputTokens, md.OutputTokens, md.TotalTokens)
	}
}

// encodeBedrockEvent produces a single AWS event-stream frame for use in tests.
// It uses the SDK's Encoder, which is the same code path AWS uses on the wire,
// so a test failure here would mean either AWS's encoding changed or our
// decoder usage is wrong — both worth catching.
func encodeBedrockEvent(t *testing.T, w io.Writer, eventType string, payload interface{}) {
	t.Helper()
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	enc := eventstream.NewEncoder()
	msg := eventstream.Message{
		Headers: eventstream.Headers{
			{Name: ":message-type", Value: eventstream.StringValue("event")},
			{Name: ":event-type", Value: eventstream.StringValue(eventType)},
			{Name: ":content-type", Value: eventstream.StringValue("application/json")},
		},
		Payload: payloadJSON,
	}
	if err := enc.Encode(w, msg); err != nil {
		t.Fatalf("encode %s frame: %v", eventType, err)
	}
}

func TestBedrock_ParseStreamingResponse(t *testing.T) {
	b := NewBedrockProxy()

	// Compose a realistic Converse stream: contentBlockDelta x2, messageStop,
	// metadata (terminal usage), in order.
	var buf bytes.Buffer
	encodeBedrockEvent(t, &buf, "messageStart", map[string]any{"role": "assistant"})
	encodeBedrockEvent(t, &buf, "contentBlockDelta", map[string]any{"delta": map[string]string{"text": "hello"}})
	encodeBedrockEvent(t, &buf, "contentBlockDelta", map[string]any{"delta": map[string]string{"text": " world"}})
	encodeBedrockEvent(t, &buf, "messageStop", map[string]any{"stopReason": "end_turn"})
	encodeBedrockEvent(t, &buf, "metadata", map[string]any{
		"usage": map[string]int{
			"inputTokens": 11, "outputTokens": 4, "totalTokens": 15,
		},
		"metrics": map[string]int{"latencyMs": 1234},
	})

	md, err := b.ParseResponseMetadata(&buf, true)
	if err != nil {
		t.Fatalf("ParseResponseMetadata streaming: %v", err)
	}
	if md.Provider != "bedrock" {
		t.Errorf("provider: %q", md.Provider)
	}
	if !md.IsStreaming {
		t.Error("IsStreaming should be true")
	}
	if md.InputTokens != 11 || md.OutputTokens != 4 || md.TotalTokens != 15 {
		t.Errorf("usage from eventstream: in=%d out=%d total=%d", md.InputTokens, md.OutputTokens, md.TotalTokens)
	}
	if md.FinishReason != "end_turn" {
		t.Errorf("finish: %q", md.FinishReason)
	}
}

func TestBedrock_ParseStreamingResponse_TruncatedBeforeMetadata(t *testing.T) {
	b := NewBedrockProxy()
	var buf bytes.Buffer
	// Only messageStart + a single delta. No metadata frame.
	encodeBedrockEvent(t, &buf, "messageStart", map[string]any{"role": "assistant"})
	encodeBedrockEvent(t, &buf, "contentBlockDelta", map[string]any{"delta": map[string]string{"text": "partial"}})
	md, err := b.ParseResponseMetadata(&buf, true)
	if err != nil {
		t.Fatalf("truncated stream: %v", err)
	}
	if md == nil {
		t.Fatal("metadata should be non-nil even when truncated")
	}
	if md.InputTokens != 0 || md.OutputTokens != 0 {
		t.Errorf("usage should be zero when metadata absent, got in=%d out=%d", md.InputTokens, md.OutputTokens)
	}
}

func TestBedrock_ParseStreamingResponse_EmptyBody(t *testing.T) {
	b := NewBedrockProxy()
	_, err := b.ParseResponseMetadata(bytes.NewReader(nil), true)
	if err == nil {
		t.Error("expected error on empty streaming body")
	}
}

// ---------------------------------------------------------------------------
// Provider interface compliance
// ---------------------------------------------------------------------------

func TestBedrock_ImplementsProvider(t *testing.T) {
	var _ Provider = (*BedrockProxy)(nil)
}

func TestBedrock_ValidateAPIKey_NoOp(t *testing.T) {
	b := NewBedrockProxy()
	req := httptest.NewRequest(http.MethodPost, "http://localhost/bedrock/model/foo/converse", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 some-signature-the-proxy-must-not-touch")
	if err := b.ValidateAPIKey(req, nil); err != nil {
		t.Errorf("ValidateAPIKey should be a no-op, got: %v", err)
	}
	// Authorization header must not be mutated.
	if !strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 some-signature-") {
		t.Error("Authorization header should not be mutated by ValidateAPIKey")
	}
}

func TestBedrock_GetHealthStatus_IncludesRegion(t *testing.T) {
	t.Setenv("AWS_REGION", "us-east-2")
	b := NewBedrockProxy()
	hs := b.GetHealthStatus()
	if hs["region"] != "us-east-2" {
		t.Errorf("region: %v", hs["region"])
	}
	if hs["provider"] != "bedrock" {
		t.Errorf("provider: %v", hs["provider"])
	}
	if hs["auth"] != "client_sigv4_passthrough" {
		t.Errorf("auth: %v", hs["auth"])
	}
}

// Used to ensure the wireProxyToFake helper compiles when not referenced
// in some test runs (e.g. when running with -run=Foo).
var _ context.Context = context.Background()
