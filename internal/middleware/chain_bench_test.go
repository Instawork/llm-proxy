package middleware

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/modelstatusstats"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/Instawork/llm-proxy/internal/ratelimitstats"
	"github.com/gorilla/mux"
)

// cannedUpstream is a RoundTripper that drains the request and returns a
// fixed body, so benchmarks measure proxy + middleware overhead rather than
// network or vendor latency.
type cannedUpstream struct {
	body        []byte
	contentType string
}

func (c *cannedUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	h := http.Header{}
	h.Set("Content-Type", c.contentType)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(c.body)),
		ContentLength: int64(len(c.body)),
		Request:       req,
	}, nil
}

func benchOpenAINonStreamingBody() []byte {
	return []byte(`{"id":"chatcmpl-bench","object":"chat.completion","created":1700000000,"model":"gpt-4o-mini-2024-07-18",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"Benchmark response text that is long enough to resemble a real completion."},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":42,"completion_tokens":18,"total_tokens":60}}`)
}

func benchOpenAISSEBody(chunks int) []byte {
	var b strings.Builder
	for i := 0; i < chunks; i++ {
		fmt.Fprintf(&b, "data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini-2024-07-18\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"tok%d \"},\"finish_reason\":null}]}\n\n", i)
	}
	b.WriteString("data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini-2024-07-18\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	b.WriteString("data: {\"id\":\"chatcmpl-bench\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini-2024-07-18\",\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":18,\"total_tokens\":60}}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

// newBenchChain wires the OpenAI provider behind the same middleware stack
// (and order) that cmd/llm-proxy builds for provider routes, minus the
// pieces that need external services (API key store, PII redactor, ID gate,
// circuit breaker). Keep this in sync with runServer when the chain changes.
func newBenchChain(tb testing.TB, upstream http.RoundTripper) http.Handler {
	tb.Helper()

	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "memory"
	cfg.Features.RateLimiting.Estimation.BytesPerToken = 4
	cfg.Features.RateLimiting.Estimation.MaxSampleBytes = 20000
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 1 << 30
	cfg.Features.RateLimiting.Limits.TokensPerMinute = 1 << 30

	pm := providers.NewProviderManager()
	openai := providers.NewOpenAIProxy()
	openai.WrapTransport(func(http.RoundTripper) http.RoundTripper { return upstream })
	pm.RegisterProvider(openai)

	r := mux.NewRouter()
	r.Use(AbortLoggingMiddleware())
	r.Use(MetaURLRewritingMiddleware(pm))
	r.Use(VendorPathPolicyMiddleware(pm))
	r.Use(ModelStatusMiddleware(pm, cfg, modelstatusstats.NewRecorder(), nil))
	r.Use(CostLimitMiddleware(pm, coststats.NewRecorder(), CostLimitOptions{
		Estimation: providers.NewYAMLConfigEstimationAdapter(cfg.Features.RateLimiting.Estimation),
	}))
	r.Use(LoggingMiddleware(pm))
	r.Use(RateLimitingMiddleware(pm, cfg, ratelimit.NewMemoryLimiter(cfg), ratelimitstats.NewRecorder()))
	r.Use(CORSMiddleware(pm))
	r.Use(TokenParsingMiddlewareWithUnmetered(pm, func(*http.Request, int) {}, func(*http.Request, *providers.LLMResponseMetadata) {}))
	r.Use(PIIResponseRestoreMiddleware(pm))
	r.Use(StreamingMiddleware(pm))

	r.PathPrefix("/openai/").Handler(openai.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	return r
}

func benchRequest(stream bool) *http.Request {
	body := `{"model":"gpt-4o-mini","messages":[{"role":"system","content":"You are a helpful assistant."},{"role":"user","content":"Summarize the plot of a novel in three sentences, please."}],"max_tokens":128,"temperature":0.2`
	if stream {
		body += `,"stream":true,"stream_options":{"include_usage":true}`
	}
	body += `}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-bench-not-a-real-key")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req
}

func runChainBenchmark(b *testing.B, stream bool, upstream *cannedUpstream) {
	// The chain logs per request (and per SSE chunk); writing that to the
	// test output would dominate the measurement.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(prev) })

	h := newBenchChain(b, upstream)

	// Warm once and assert the path actually proxies so a routing regression
	// cannot masquerade as a speedup.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, benchRequest(stream))
	if rec.Code != http.StatusOK {
		b.Fatalf("warm-up status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("gpt-4o-mini")) {
		b.Fatalf("warm-up body does not look like an upstream response: %s", rec.Body.String())
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, benchRequest(stream))
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d", rec.Code)
		}
	}
}

func BenchmarkChain_ChatCompletions_NonStreaming(b *testing.B) {
	runChainBenchmark(b, false, &cannedUpstream{body: benchOpenAINonStreamingBody(), contentType: "application/json"})
}

func BenchmarkChain_ChatCompletions_SSE40Chunks(b *testing.B) {
	runChainBenchmark(b, true, &cannedUpstream{body: benchOpenAISSEBody(40), contentType: "text/event-stream"})
}

func BenchmarkChain_ChatCompletions_SSE400Chunks(b *testing.B) {
	runChainBenchmark(b, true, &cannedUpstream{body: benchOpenAISSEBody(400), contentType: "text/event-stream"})
}
