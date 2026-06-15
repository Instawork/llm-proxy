package circuit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ─── Test fakes ───────────────────────────────────────────────────────────

// fakeMetrics records every Incr call so tests can assert on metric
// names and tag sets.
type fakeMetrics struct {
	mu    sync.Mutex
	calls []fakeMetricsCall
}

type fakeMetricsCall struct {
	Name string
	Tags []string
	Rate float64
}

func (m *fakeMetrics) Incr(name string, tags []string, rate float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Copy tags so callers that mutate their slice after Incr don't
	// retroactively change recorded events.
	tagsCopy := append([]string(nil), tags...)
	m.calls = append(m.calls, fakeMetricsCall{Name: name, Tags: tagsCopy, Rate: rate})
	return nil
}

func (m *fakeMetrics) findByName(name string) []fakeMetricsCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []fakeMetricsCall
	for _, c := range m.calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// captureLogs returns a slog.Logger whose JSON-encoded output is
// written into the supplied buffer so tests can grep structured
// attributes without parsing slog text format.
func captureLogs(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// findLogLine returns the first JSON-decoded log entry whose "msg"
// matches substr, or nil if none.
func findLogLine(t *testing.T, buf *bytes.Buffer, substr string) map[string]any {
	t.Helper()
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		msg, _ := entry["msg"].(string)
		if strings.Contains(msg, substr) {
			return entry
		}
	}
	return nil
}

// dummyOpenAIRequest crafts a request whose body advertises an OpenAI
// model name so the model extractor can echo it back.
func dummyOpenAIRequest(model string) *http.Request {
	body := `{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// fakeModelFn returns a static model regardless of request — useful
// when we only care that the wiring threads it through to the log.
func fakeModelFn(model string) ModelFromRequestFunc {
	return func(_ *http.Request) string { return model }
}

// ─── ClassifyFailureKind / classifyTransportErrorKind ────────────────────

func TestClassifyFailureKind_HTTPStatuses(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		status   int
		want     FailureKind
	}{
		{"500", "openai", 500, KindHTTP500},
		{"502", "anthropic", 502, KindHTTP502},
		{"503", "gemini", 503, KindHTTP503},
		{"504", "openai", 504, KindHTTP504},
		{"529 Anthropic overloaded", "anthropic", 529, KindHTTPOverloaded},
		{"507 misc 5xx", "openai", 507, KindHTTP5xxOther},
		{"400 bad request", "openai", 400, KindHTTP4xx},
		{"401 auth", "anthropic", 401, KindHTTP4xx},
		{"200 success", "openai", 200, KindNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fk := ClassifyFailureKind(tc.provider, resp(tc.status, nil), nil)
			if fk != tc.want {
				t.Fatalf("ClassifyFailureKind(%q, %d) = %q, want %q", tc.provider, tc.status, fk, tc.want)
			}
		})
	}
}

func TestClassifyFailureKind_429_LocalVsGlobal(t *testing.T) {
	// Local: bucket still has capacity.
	local := resp(429, map[string]string{
		"anthropic-ratelimit-requests-remaining": "10",
		"anthropic-ratelimit-tokens-remaining":   "0",
	})
	if got := ClassifyFailureKind("anthropic", local, nil); got != KindHTTP429Local {
		t.Fatalf("local 429 → want %q, got %q", KindHTTP429Local, got)
	}

	// Global: both buckets exhausted.
	global := resp(429, map[string]string{
		"anthropic-ratelimit-requests-remaining": "0",
		"anthropic-ratelimit-tokens-remaining":   "0",
	})
	if got := ClassifyFailureKind("anthropic", global, nil); got != KindHTTP429Global {
		t.Fatalf("global 429 → want %q, got %q", KindHTTP429Global, got)
	}

	// Quota: OpenAI 429 whose body carries error.code=insufficient_quota.
	quota := respWithBody(429, nil, `{"error":{"code":"insufficient_quota"}}`)
	if got := ClassifyFailureKind("openai", quota, nil); got != KindHTTP429Quota {
		t.Fatalf("insufficient_quota 429 → want %q, got %q", KindHTTP429Quota, got)
	}
}

func TestClassifyTransportErrorKind_DistinctLabels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want FailureKind
	}{
		{"deadline exceeded", context.DeadlineExceeded, KindClientDeadline},
		{"client canceled", context.Canceled, KindClientCanceled},
		{"unexpected eof", io.ErrUnexpectedEOF, KindUnexpectedEOF},
		{"plain eof", io.EOF, KindEOF},
		{"connection reset string", errors.New("read tcp 127.0.0.1:0: connection reset by peer"), KindConnectionReset},
		{"connection refused", errors.New("dial tcp: connection refused"), KindConnectionRefused},
		{"dns failure", errors.New("dial tcp: lookup foo: no such host"), KindDNSFailure},
		{"tls handshake", errors.New("tls handshake error: bad cert"), KindTLSHandshake},
		{"i/o timeout", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}, KindIOTimeout},
		{"net.Error catch-all", &net.AddrError{Err: "invalid", Addr: "127.0.0.1"}, KindNetworkOther},
		{"unknown transport", errors.New("something inscrutable"), KindUnknownTransport},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTransportErrorKind(tc.err); got != tc.want {
				t.Fatalf("kind(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestClassify_DeadlineExceeded_KindIsDistinct nails down the explicit
// regression that "[was] not specifically tagged as a client-side
// deadline" — we want to be able to subtract this signal out of the
// provider-degradation dashboard even though the underlying
// FailureClass remains FailureClassDegraded so the breaker still
// trips on sustained client-side timeouts (which usually means the
// upstream is slow enough to matter).
func TestClassify_DeadlineExceeded_KindIsDistinct(t *testing.T) {
	fc := ClassifyResponse("gemini", nil, context.DeadlineExceeded)
	if fc != FailureClassDegraded {
		t.Fatalf("ClassifyResponse should still return Degraded for deadlines, got %s", fc)
	}
	fk := ClassifyFailureKind("gemini", nil, context.DeadlineExceeded)
	if fk != KindClientDeadline {
		t.Fatalf("ClassifyFailureKind should return KindClientDeadline, got %s", fk)
	}
	// Sanity: a 504 must not collapse into the deadline kind even
	// though they sometimes look the same in dashboards.
	if got := ClassifyFailureKind("gemini", resp(504, nil), nil); got != KindHTTP504 {
		t.Fatalf("504 must classify as KindHTTP504, got %s", got)
	}
}

// ─── Log-mode terminal_failure_observed enrichment ────────────────────────

func TestObserveOnly_TerminalFailureLogContainsStructuredAttrs(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled: true,
		Mode:    ModeLog,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(
		inner, store, cfg, "openai", log,
		WithMetrics(metrics),
		WithModelExtractor(fakeModelFn("gpt-4o")),
	)

	resp, err := tr.RoundTrip(dummyOpenAIRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("log mode must pass real upstream 503 through, got %d", resp.StatusCode)
	}

	entry := findLogLine(t, buf, "terminal_failure_observed")
	if entry == nil {
		t.Fatalf("expected a terminal_failure_observed log line, got: %s", buf.String())
	}
	wantStr := map[string]string{
		"provider":     "openai",
		"model":        "gpt-4o",
		"path":         "/openai/v1/chat/completions",
		"method":       http.MethodPost,
		"failure_kind": string(KindHTTP503),
		"mode":         ModeLog,
	}
	for k, v := range wantStr {
		if got, _ := entry[k].(string); got != v {
			t.Fatalf("log attr %q: want %q, got %q (entry=%v)", k, v, got, entry)
		}
	}
	if got, _ := entry["status_code"].(float64); int(got) != 503 {
		t.Fatalf("log attr status_code: want 503, got %v", entry["status_code"])
	}

	// Metric: circuit.terminal_failure with the same dimensions.
	calls := metrics.findByName("circuit.terminal_failure")
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 circuit.terminal_failure metric, got %d (%+v)", len(calls), metrics.calls)
	}
	tags := tagSet(calls[0].Tags)
	for _, want := range []string{
		"provider:openai",
		"model:gpt-4o",
		"status_code:503",
		"failure_kind:" + string(KindHTTP503),
	} {
		if _, ok := tags[want]; !ok {
			t.Fatalf("missing tag %q on circuit.terminal_failure (tags=%v)", want, calls[0].Tags)
		}
	}
}

// TestFastFailMetric_TagsCaller verifies the caller-label dimension wired
// via WithCallerExtractor lands on both the log line and the dogstatsd tag
// set, so operators can attribute degraded responses to a downstream
// client (e.g. "finch-prod").
func TestFastFailMetric_TagsCaller(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("inner transport must not be called when the circuit is open")
		return nil, nil
	})
	cfg := Config{Enabled: true, Mode: ModeEnforce}.Defaults()
	store := NewMemoryStore(cfg)
	// Force the bare-provider breaker open so the next request fast-fails.
	if err := store.ForceOpen(context.Background(), "openai", cfg.CooldownSeconds); err != nil {
		t.Fatalf("ForceOpen: %v", err)
	}
	tr := NewTransport(
		inner, store, cfg, "openai", log,
		WithMetrics(metrics),
		WithCallerExtractor(func(*http.Request) string { return "finch-prod" }),
	)

	resp, err := tr.RoundTrip(dummyOpenAIRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	calls := metrics.findByName("circuit.fast_fail")
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 circuit.fast_fail metric, got %d (%+v)", len(calls), metrics.calls)
	}
	if _, ok := tagSet(calls[0].Tags)["caller:finch-prod"]; !ok {
		t.Fatalf("missing tag caller:finch-prod on circuit.fast_fail (tags=%v)", calls[0].Tags)
	}
}

// TestObserveOnly_TerminalFailureMetric_TransportError_TagsClientDeadline
// is the explicit fixture for the "tag context.DeadlineExceeded
// distinctly" requirement: a deadline-exceeded transport error must
// surface with status_code=0 and failure_kind=client_deadline_exceeded
// in both the log line and the dogstatsd tag set, so dashboards can
// subtract this from the provider-degradation signal.
func TestObserveOnly_TerminalFailureMetric_TagsClientDeadline(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})
	cfg := Config{
		Enabled: true,
		Mode:    ModeLog,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(
		inner, store, cfg, "gemini", log,
		WithMetrics(metrics),
		WithModelExtractor(fakeModelFn("gemini-2.5-flash")),
	)

	if _, err := tr.RoundTrip(dummyOpenAIRequest("gemini-2.5-flash")); err == nil {
		t.Fatal("expected error from inner transport, got nil")
	}

	calls := metrics.findByName("circuit.terminal_failure")
	if len(calls) != 1 {
		t.Fatalf("expected 1 circuit.terminal_failure call, got %d", len(calls))
	}
	tags := tagSet(calls[0].Tags)
	if _, ok := tags["status_code:0"]; !ok {
		t.Fatalf("transport-level failure must carry status_code:0 (tags=%v)", calls[0].Tags)
	}
	if _, ok := tags["failure_kind:"+string(KindClientDeadline)]; !ok {
		t.Fatalf("client-deadline must tag failure_kind=%s (tags=%v)", KindClientDeadline, calls[0].Tags)
	}
}

// ─── Enforce-mode handleTerminalFailure ───────────────────────────────────

func TestEnforce_HandleTerminalFailure_PreservesLastFailureContext(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	calls := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return makeResp(502), nil
	})
	// MaxTransientRetries:1 means: try once + retry once → 2 inner
	// calls before declaring terminal.  This is the smallest valid
	// value that exercises handleTerminalFailure (Defaults() rewrites
	// 0 → 2, so we must pick a positive value explicitly).  The
	// retry's transientBackoff(0) sleeps ~500ms which keeps the test
	// fast.
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(
		inner, store, cfg, "anthropic", log,
		WithMetrics(metrics),
		WithModelExtractor(fakeModelFn("claude-3-5-sonnet-20240620")),
	)

	resp, err := tr.RoundTrip(dummyOpenAIRequest("ignored"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want synthetic 503, got %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()              //nolint:errcheck

	if calls != 2 {
		t.Fatalf("expected exactly 2 inner round-trips (1 + 1 retry), got %d", calls)
	}

	terminalEntry := findLogLine(t, buf, "terminal failure, returning degraded signal")
	if terminalEntry == nil {
		t.Fatalf("expected terminal failure log line; got %s", buf.String())
	}
	if got, _ := terminalEntry["failure_kind"].(string); got != string(KindHTTP502) {
		t.Fatalf("terminal failure log: want failure_kind=%s, got %q", KindHTTP502, got)
	}
	if got, _ := terminalEntry["status_code"].(float64); int(got) != 502 {
		t.Fatalf("terminal failure log: want status_code=502, got %v", terminalEntry["status_code"])
	}
	if got, _ := terminalEntry["model"].(string); got != "claude-3-5-sonnet-20240620" {
		t.Fatalf("terminal failure log: want model=claude-3-5-sonnet-20240620, got %q", got)
	}
	if got, _ := terminalEntry["new_state"].(string); got != "open" {
		t.Fatalf("terminal failure log: want new_state=open at threshold=1, got %q", got)
	}

	thresholdEntry := findLogLine(t, buf, "threshold crossed")
	if thresholdEntry == nil {
		t.Fatalf("expected threshold-crossed log line; got %s", buf.String())
	}
	if got, _ := thresholdEntry["failure_kind"].(string); got != string(KindHTTP502) {
		t.Fatalf("threshold log must reuse failure_kind from last attempt, got %q", got)
	}

	terminalCalls := metrics.findByName("circuit.terminal_failure")
	if len(terminalCalls) != 1 {
		t.Fatalf("expected one circuit.terminal_failure call, got %d", len(terminalCalls))
	}
	tags := tagSet(terminalCalls[0].Tags)
	for _, want := range []string{
		"provider:anthropic",
		"model:claude-3-5-sonnet-20240620",
		"status_code:502",
		"failure_kind:" + string(KindHTTP502),
	} {
		if _, ok := tags[want]; !ok {
			t.Fatalf("missing tag %q on circuit.terminal_failure (tags=%v)", want, terminalCalls[0].Tags)
		}
	}
}

// TestEnforce_FastFailMetric_OnOpenCircuit verifies that a synthesized
// 503 (because the circuit is already open) increments
// circuit.fast_fail rather than circuit.terminal_failure — the two
// must remain distinct so dashboards can answer "are we
// fast-failing?" separately from "did we observe an upstream failure
// just now?".
//
// Also pins the failure_kind=circuit_open contract so dashboards can
// distinguish "we deliberately didn't try" (this case) from
// failure_kind=nil_response, which means the upstream was attempted
// but returned nothing.
func TestEnforce_FastFailMetric_OnOpenCircuit(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	state, _, err := store.RecordTerminalFailure(context.Background(), "openai:gpt-4o") //nolint:errcheck
	if state != StateOpen {
		t.Fatalf("expected state to be Open after RecordTerminalFailure, got %v", state)
	}

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatal("inner transport must not be called when circuit is open")
		return nil, nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", log,
		WithMetrics(metrics),
		WithModelExtractor(fakeModelFn("gpt-4o")),
	)

	resp, err := tr.RoundTrip(dummyOpenAIRequest("gpt-4o"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected synthetic 503, got %d", resp.StatusCode)
	}

	fastFailCalls := metrics.findByName("circuit.fast_fail")
	if len(fastFailCalls) != 1 {
		t.Fatalf("expected exactly one circuit.fast_fail metric, got %d (%+v)", len(fastFailCalls), metrics.calls)
	}
	tags := tagSet(fastFailCalls[0].Tags)
	if _, ok := tags["failure_kind:"+string(KindCircuitOpen)]; !ok {
		t.Fatalf("fast-fail must tag failure_kind=circuit_open (tags=%v)", fastFailCalls[0].Tags)
	}
	if _, ok := tags["status_code:0"]; !ok {
		t.Fatalf("fast-fail must tag status_code:0 (no upstream response) (tags=%v)", fastFailCalls[0].Tags)
	}

	if got := metrics.findByName("circuit.terminal_failure"); len(got) != 0 {
		t.Fatalf("circuit.fast_fail must NOT also emit circuit.terminal_failure; got %d", len(got))
	}

	entry := findLogLine(t, buf, "fast-fail per-model breaker open")
	if entry == nil {
		t.Fatalf("expected fast-fail log line; got %s", buf.String())
	}
	if got, _ := entry["failure_kind"].(string); got != string(KindCircuitOpen) {
		t.Fatalf("fast-fail log: want failure_kind=%s, got %q", KindCircuitOpen, got)
	}
}

// TestProbeFailed_Metric_TagsModelFromCachedBody pins the contract that
// runProbe calls cacheBody before TryStartProbe so the probe-failure
// observability path can attribute the failure to a model.  Without
// the cacheBody call the modelFn guard in newFailureContext (which
// requires req.GetBody != nil to avoid unbounded body reads on the
// fast-fail path) would correctly skip extraction and tag
// model:"unknown" on every probe failure — useless on dashboards
// trying to break out which models are unhealthy.
func TestProbeFailed_Metric_TagsModelFromCachedBody(t *testing.T) {
	buf := &bytes.Buffer{}
	log := captureLogs(buf)
	metrics := &fakeMetrics{}

	cfg := Config{
		Enabled:          true,
		Mode:             ModeEnforce,
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  300,
	}.Defaults()
	store := NewMemoryStore(cfg)

	// Force the store into half-open so the next call routes through
	// runProbe rather than the fast-fail or retry paths.
	e := store.entry("openai:gpt-4o")
	e.mu.Lock()
	e.state = StateHalfOpen
	e.mu.Unlock()

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", log,
		WithMetrics(metrics),
		WithModelExtractor(fakeModelFn("gpt-4o")),
	)

	// Use a body that http.NewRequest does NOT special-case for GetBody
	// (a bare io.Reader), so cacheBody is the only thing that can make
	// the body replayable for the model extractor.
	req, err := http.NewRequest(http.MethodPost, "/openai/v1/chat/completions",
		struct{ io.Reader }{Reader: bytes.NewReader([]byte(`{"model":"gpt-4o"}`))})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if req.GetBody != nil {
		t.Fatal("test setup invariant broken: GetBody must be nil so the test exercises the cacheBody path")
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 after probe failure, got %d", resp.StatusCode)
	}

	calls := metrics.findByName("circuit.probe_failed")
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 circuit.probe_failed metric, got %d (%+v)", len(calls), metrics.calls)
	}
	tags := tagSet(calls[0].Tags)
	if _, ok := tags["model:gpt-4o"]; !ok {
		t.Fatalf("probe_failed must tag model:gpt-4o (tags=%v)", calls[0].Tags)
	}

	entry := findLogLine(t, buf, "probe failed, re-opening circuit")
	if entry == nil {
		t.Fatalf("expected probe-failed log line; got %s", buf.String())
	}
	if got, _ := entry["model"].(string); got != "gpt-4o" {
		t.Fatalf("probe_failed log: want model=gpt-4o, got %q", got)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func tagSet(tags []string) map[string]struct{} {
	out := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		out[t] = struct{}{}
	}
	return out
}
