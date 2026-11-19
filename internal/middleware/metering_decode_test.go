package middleware

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"

	"github.com/Instawork/llm-proxy/internal/cost"
	"github.com/Instawork/llm-proxy/internal/providers"
)

const anthropicMessageJSON = `{"id":"msg_01Brotli","type":"message","role":"assistant","model":"claude-haiku-5-5",` +
	`"content":[{"type":"text","text":"Payment system: Stripe"}],"stop_reason":"end_turn",` +
	`"usage":{"input_tokens":250000,"output_tokens":170}}`

const anthropicMessageSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_01BrotliStream","type":"message","role":"assistant","model":"claude-haiku-5-5","content":[],"usage":{"input_tokens":250000,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Stripe"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":170}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

const openAIChatJSON = `{"id":"chatcmpl-brotli","object":"chat.completion","model":"gpt-4o-mini",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}}`

const openAIChatSSE = `data: {"id":"chatcmpl-brotli","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
	`data: {"id":"chatcmpl-brotli","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}}` + "\n\n" +
	"data: [DONE]\n\n"

// redirectTransport sends the provider's outbound request to a local test
// upstream while keeping the provider's own *http.Transport underneath, so
// Accept-Encoding negotiation and transparent decompression behave as in
// production.
type redirectTransport struct {
	inner  http.RoundTripper
	target *url.URL
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme = rt.target.Scheme
	out.URL.Host = rt.target.Host
	out.Host = rt.target.Host
	return rt.inner.RoundTrip(out)
}

type transportWrapper interface {
	providers.Provider
	WrapTransport(func(http.RoundTripper) http.RoundTripper)
}

type metricCall struct {
	name string
	tags []string
}

type recordingMetrics struct {
	mu    sync.Mutex
	calls []metricCall
}

func (m *recordingMetrics) Incr(name string, tags []string, _ float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, metricCall{name: name, tags: slices.Clone(tags)})
	return nil
}

func (m *recordingMetrics) Distribution(string, float64, []string, float64) error { return nil }

type recordingCostTransport struct {
	mu      sync.Mutex
	records []*cost.CostRecord
}

func (r *recordingCostTransport) WriteRecord(record *cost.CostRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	return nil
}

func mustBrotli(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, 5)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("brotli Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("brotli Close: %v", err)
	}
	return buf.Bytes()
}

type meteredRun struct {
	rec            *httptest.ResponseRecorder
	upstreamAccept string
	records        []*cost.CostRecord
	metrics        []metricCall
	unmetered      int
}

// runMetered drives one request through TokenParsingMiddlewareWithOptions and
// the real provider proxy to an upstream that answers with wireBody and the
// given Content-Encoding, then tracks cost with a real CostTracker.
func runMetered(t *testing.T, provider transportWrapper, path, reqBody, clientAcceptEncoding, contentType, contentEncoding string, wireBody []byte) meteredRun {
	t.Helper()
	var run meteredRun
	var acceptMu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptMu.Lock()
		run.upstreamAccept = strings.Join(r.Header.Values("Accept-Encoding"), ", ")
		acceptMu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", contentType)
		if contentEncoding != "" {
			w.Header().Set("Content-Encoding", contentEncoding)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wireBody)
	}))
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	provider.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		return redirectTransport{inner: inner, target: target}
	})

	costSink := &recordingCostTransport{}
	tracker := cost.NewCostTracker(costSink)
	tracker.SetPricingForModel("anthropic", "claude-haiku-5-5", &cost.ModelPricing{Tiers: []cost.PricingTier{
		{Threshold: 100_000, Input: 0.10, Output: 0.50},
		{Threshold: 0, Input: 0.50, Output: 2.50},
	}})
	tracker.SetPricingForModel("openai", "gpt-4o-mini", &cost.ModelPricing{Tiers: []cost.PricingTier{
		{Threshold: 0, Input: 0.15, Output: 0.60},
	}})
	trackCost := func(r *http.Request, md *providers.LLMResponseMetadata) {
		if err := tracker.TrackRequest(md, "user", "127.0.0.1", r.URL.Path, ""); err != nil {
			t.Errorf("TrackRequest: %v", err)
		}
	}

	metrics := &recordingMetrics{}
	pm := providers.NewProviderManager()
	pm.RegisterProvider(provider)
	chain := TokenParsingMiddlewareWithOptions(pm, TokenParsingOptions{
		OnUnmetered: func(*http.Request, int) { run.unmetered++ },
		Metrics:     metrics,
	}, trackCost)(provider.Proxy())

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	if clientAcceptEncoding != "" {
		req.Header.Set("Accept-Encoding", clientAcceptEncoding)
	}
	run.rec = httptest.NewRecorder()
	captureLogOutput(func() { chain.ServeHTTP(run.rec, req) })

	run.records = costSink.records
	run.metrics = metrics.calls
	return run
}

// TestMetering_CompressedResponses is the regression test for the Nov 2026
// unmetered Haiku spend: finch's httpx clients offer br, Anthropic answers
// non-streaming calls with Content-Encoding: br, and the parser used to see
// raw Brotli bytes. Every case must meter the right tokens and cost while the
// client still receives the exact upstream bytes.
func TestMetering_CompressedResponses(t *testing.T) {
	const finchAcceptEncoding = "gzip, deflate, br, zstd"

	cases := []struct {
		name        string
		provider    func() transportWrapper
		path        string
		reqBody     string
		contentType string
		plain       string
		encoding    string
		wantModel   string
		wantIn      int
		wantOut     int
		// CostTracker rounds each component up to 4 decimals: Haiku 5.5
		// over 100k is $0.125 + $0.0005; gpt-4o-mini is $0.0002 + $0.0003.
		wantCost float64
	}{
		{
			name:     "anthropic non-streaming br",
			provider: func() transportWrapper { return providers.NewAnthropicProxy() },
			path:     "/anthropic/v1/messages", reqBody: `{"model":"claude-haiku-5-5","max_tokens":256}`,
			contentType: "application/json", plain: anthropicMessageJSON, encoding: "br",
			wantModel: "claude-haiku-5-5", wantIn: 250000, wantOut: 170, wantCost: 0.1255,
		},
		{
			name:     "anthropic streaming br",
			provider: func() transportWrapper { return providers.NewAnthropicProxy() },
			path:     "/anthropic/v1/messages", reqBody: `{"model":"claude-haiku-5-5","max_tokens":256,"stream":true}`,
			contentType: "text/event-stream", plain: anthropicMessageSSE, encoding: "br",
			wantModel: "claude-haiku-5-5", wantIn: 250000, wantOut: 170, wantCost: 0.1255,
		},
		{
			name:     "anthropic non-streaming gzip",
			provider: func() transportWrapper { return providers.NewAnthropicProxy() },
			path:     "/anthropic/v1/messages", reqBody: `{"model":"claude-haiku-5-5","max_tokens":256}`,
			contentType: "application/json", plain: anthropicMessageJSON, encoding: "gzip",
			wantModel: "claude-haiku-5-5", wantIn: 250000, wantOut: 170, wantCost: 0.1255,
		},
		{
			name:     "openai non-streaming br",
			provider: func() transportWrapper { return providers.NewOpenAIProxy() },
			path:     "/openai/v1/chat/completions", reqBody: `{"model":"gpt-4o-mini","messages":[]}`,
			contentType: "application/json", plain: openAIChatJSON, encoding: "br",
			wantModel: "gpt-4o-mini", wantIn: 1000, wantOut: 500, wantCost: 0.0005,
		},
		{
			name:     "openai streaming br",
			provider: func() transportWrapper { return providers.NewOpenAIProxy() },
			path:     "/openai/v1/chat/completions", reqBody: `{"model":"gpt-4o-mini","messages":[],"stream":true}`,
			contentType: "text/event-stream", plain: openAIChatSSE, encoding: "br",
			wantModel: "gpt-4o-mini", wantIn: 1000, wantOut: 500, wantCost: 0.0005,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var wire []byte
			switch tc.encoding {
			case "br":
				wire = mustBrotli(t, []byte(tc.plain))
			case "gzip":
				wire = mustGzip(t, []byte(tc.plain))
			}
			run := runMetered(t, tc.provider(), tc.path, tc.reqBody, finchAcceptEncoding, tc.contentType, tc.encoding, wire)

			if !bytes.Equal(run.rec.Body.Bytes(), wire) {
				t.Errorf("client body was altered: got %d bytes, want the %d upstream bytes", run.rec.Body.Len(), len(wire))
			}
			if got := run.rec.Header().Get("Content-Encoding"); got != tc.encoding {
				t.Errorf("client Content-Encoding = %q, want %q", got, tc.encoding)
			}
			if run.upstreamAccept != "gzip, deflate, br" {
				t.Errorf("upstream Accept-Encoding = %q, want zstd narrowed out", run.upstreamAccept)
			}
			if len(run.metrics) != 0 {
				t.Errorf("unexpected unparseable metric: %+v", run.metrics)
			}
			if run.unmetered != 0 {
				t.Errorf("request reported unmetered %d times", run.unmetered)
			}
			if len(run.records) != 1 {
				t.Fatalf("want 1 cost record, got %d", len(run.records))
			}
			r := run.records[0]
			if r.Model != tc.wantModel || r.InputTokens != tc.wantIn || r.OutputTokens != tc.wantOut {
				t.Errorf("record = model %q in %d out %d, want %q %d %d", r.Model, r.InputTokens, r.OutputTokens, tc.wantModel, tc.wantIn, tc.wantOut)
			}
			if diff := r.TotalCost - tc.wantCost; diff > 1e-12 || diff < -1e-12 {
				t.Errorf("TotalCost = %v, want %v", r.TotalCost, tc.wantCost)
			}
		})
	}
}

// TestMetering_UnsupportedEncodingCountsAndPassesThrough covers an upstream
// that ignores the narrowed Accept-Encoding and answers with a coding the
// proxy cannot decode: the client still gets the bytes, and the miss is
// counted instead of disappearing.
func TestMetering_UnsupportedEncodingCountsAndPassesThrough(t *testing.T) {
	wire := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x58, 0x01, 0x02}
	run := runMetered(t, providers.NewAnthropicProxy(), "/anthropic/v1/messages",
		`{"model":"claude-haiku-5-5","max_tokens":256}`, "gzip, br, zstd", "application/json", "zstd", wire)

	if !bytes.Equal(run.rec.Body.Bytes(), wire) || run.rec.Header().Get("Content-Encoding") != "zstd" {
		t.Errorf("client response altered: body %x encoding %q", run.rec.Body.Bytes(), run.rec.Header().Get("Content-Encoding"))
	}
	if len(run.records) != 0 {
		t.Errorf("want no cost record, got %d", len(run.records))
	}
	if run.unmetered != 1 {
		t.Errorf("unmetered callback = %d, want 1", run.unmetered)
	}
	if len(run.metrics) != 1 || run.metrics[0].name != unparseableResponseMetric {
		t.Fatalf("want one %s counter, got %+v", unparseableResponseMetric, run.metrics)
	}
	tags := run.metrics[0].tags
	for _, want := range []string{"provider:anthropic", "reason:unsupported_encoding", "encoding:zstd", "streaming:false", "status_class:2xx"} {
		if !slices.Contains(tags, want) {
			t.Errorf("counter tags %v missing %q", tags, want)
		}
	}
}

// TestMetering_AcceptEncodingWithNothingDecodable checks that a client
// offering only codings the proxy cannot decode is downgraded to Go's own
// gzip negotiation: the upstream sees gzip, and the client gets a plain body.
func TestMetering_AcceptEncodingWithNothingDecodable(t *testing.T) {
	run := runMetered(t, providers.NewAnthropicProxy(), "/anthropic/v1/messages",
		`{"model":"claude-haiku-5-5","max_tokens":256}`, "zstd", "application/json", "", []byte(anthropicMessageJSON))
	if run.upstreamAccept != "gzip" {
		t.Errorf("upstream Accept-Encoding = %q, want transport-added gzip", run.upstreamAccept)
	}
	if len(run.records) != 1 || run.records[0].InputTokens != 250000 {
		t.Errorf("want metered record, got %+v", run.records)
	}
}

func TestDecodeCapturedBody(t *testing.T) {
	sse := []byte(anthropicMessageSSE)
	truncatedBrotli := mustBrotli(t, sse)
	truncatedBrotli = truncatedBrotli[:len(truncatedBrotli)-4]

	t.Run("streaming truncated keeps partial", func(t *testing.T) {
		body, reason, _ := decodeCapturedBody(truncatedBrotli, "br", true)
		if reason != "" || len(body) == 0 || !bytes.HasPrefix(sse, body) {
			t.Fatalf("reason %q, %d bytes; want a prefix of the stream", reason, len(body))
		}
	})
	t.Run("non-streaming truncated is decode_error", func(t *testing.T) {
		if _, reason, err := decodeCapturedBody(truncatedBrotli, "br", false); reason != reasonDecodeError || err == nil {
			t.Fatalf("reason %q err %v, want %q", reason, err, reasonDecodeError)
		}
	})
	t.Run("garbage gzip is decode_error", func(t *testing.T) {
		if _, reason, _ := decodeCapturedBody([]byte("not gzip"), "gzip", true); reason != reasonDecodeError {
			t.Fatalf("reason %q, want %q", reason, reasonDecodeError)
		}
	})
	t.Run("bomb is decoded_too_large", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write(make([]byte, maxDecodedBodyBytes+1))
		_ = gz.Close()
		_, reason, err := decodeCapturedBody(buf.Bytes(), "gzip", false)
		if reason != reasonDecodedTooLarge || !errors.Is(err, providers.ErrDecodedBodyTooLarge) {
			t.Fatalf("reason %q err %v, want %q", reason, err, reasonDecodedTooLarge)
		}
	})
}

func TestEncodingTagValue(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "identity",
		"br":                    "br",
		" GZIP ":                "gzip",
		"gzip, br":              "gzip+br",
		"x;y=1":                 "other",
		strings.Repeat("a", 40): "other",
	} {
		if got := encodingTagValue(in); got != want {
			t.Errorf("encodingTagValue(%q) = %q, want %q", in, got, want)
		}
	}
}
