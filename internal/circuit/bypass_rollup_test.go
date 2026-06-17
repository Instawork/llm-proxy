package circuit

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// modelFromBody is a tiny model extractor that reads `{"model":"..."}` out
// of a JSON request body.  It mirrors the shape of the real provider
// extractors (best-effort, GetBody-aware, never blocks) without pulling
// in the full provider package — keeping these tests self-contained.
func modelFromBody(req *http.Request) string {
	if req == nil || req.GetBody == nil {
		return ""
	}
	body, err := req.GetBody()
	if err != nil {
		return ""
	}
	defer body.Close() //nolint:errcheck
	b, err := io.ReadAll(body)
	if err != nil {
		return ""
	}
	const needle = `"model":"`
	idx := strings.Index(string(b), needle)
	if idx < 0 {
		return ""
	}
	rest := string(b)[idx+len(needle):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// requestForModel builds a dummyRequest variant with a body that targets
// the given model, so the modelFromBody extractor returns `model`.
func requestForModel(model string) *http.Request {
	body := `{"model":"` + model + `","messages":[]}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// bypassConfig is the shared starting Config for the bypass + rollup
// tests below.  Tests tweak individual fields rather than re-declaring
// the whole struct.
func bypassConfig() Config {
	return Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
		MaxRateLimitRetries: 0,
		BypassAllowed:       true,
	}.Defaults()
}

// ─── Bypass header behaviour ─────────────────────────────────────────────

// When the bypass header is set on an Open circuit, the inner transport
// MUST be invoked and its real response returned.  Without bypass the
// transport would fast-fail with the synthetic 503 + DegradedSignal.
func TestBypass_OpenCircuit_CallsInnerAndReturnsRealResponse(t *testing.T) {
	cfg := bypassConfig()
	store := NewMemoryStore(cfg)
	// Pre-open the breaker for the per-model key the test will hit.
	if _, _, err := store.RecordTerminalFailure(context.Background(), "openai:gpt-4o"); err != nil {
		t.Fatalf("RecordTerminalFailure: %v", err)
	}

	innerCalled := false
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		innerCalled = true
		// Bypass markers MUST NOT leak to the upstream.
		if req.Header.Get(BypassHeader) != "" {
			t.Errorf("bypass header should be stripped before upstream RoundTrip, got %q", req.Header.Get(BypassHeader))
		}
		if req.Header.Get(BypassReasonHeader) != "" {
			t.Errorf("bypass reason header should be stripped before upstream RoundTrip")
		}
		return makeResp(200), nil
	})

	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := requestForModel("gpt-4o")
	req.Header.Set(BypassHeader, "1")
	req.Header.Set(BypassReasonHeader, "no_fallback_configured")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatalf("inner transport must be invoked for bypass requests even when circuit is open")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want real upstream 200, got %d", resp.StatusCode)
	}
	// Body must NOT contain the synthetic degraded signal — bypass
	// returns whatever the upstream actually returned.
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("bypass response unexpectedly contains DegradedSignal: %s", b)
	}
}

// Bypass requests still feed observability — a 5xx returned during a
// bypass call is recorded against the breaker so dashboards stay
// accurate.  Without this the breaker would silently lose visibility
// into the upstream's true health for any caller that bypasses.
func TestBypass_DegradedResponse_StillFeedsBreaker(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 2 // need two failures to open
	store := NewMemoryStore(cfg)

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	for i := 0; i < 2; i++ {
		req := requestForModel("gpt-4o")
		req.Header.Set(BypassHeader, "true")
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		// Bypass must always return the real upstream response, even
		// after the breaker would have opened.
		if resp.StatusCode != 503 {
			t.Fatalf("call %d: want real upstream 503, got %d", i+1, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	state, err := store.GetState(context.Background(), "openai:gpt-4o")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state != StateOpen {
		t.Fatalf("breaker should be Open after bypass-observed failures crossed the threshold, got %s", state)
	}
}

// When BypassAllowed is false, the header is ignored and the breaker's
// fast-fail behaviour is preserved.  This keeps the safety valve under
// operator control rather than letting any client circumvent enforcement.
func TestBypass_DisabledByConfig_HeaderIsIgnored(t *testing.T) {
	cfg := bypassConfig()
	cfg.BypassAllowed = false
	store := NewMemoryStore(cfg)
	_, _, _ = store.RecordTerminalFailure(context.Background(), "openai:gpt-4o")

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := requestForModel("gpt-4o")
	req.Header.Set(BypassHeader, "1")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if innerCalled {
		t.Fatal("BypassAllowed=false must short-circuit, inner transport was invoked")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want fast-fail 503 with bypass disabled, got %d", resp.StatusCode)
	}
}

// Query-parameter bypass for SDKs (notably Google Gemini's client) that
// cannot set custom HTTP headers.  Behaviour must be identical to the
// header form.
func TestBypass_QueryParamEquivalent(t *testing.T) {
	cfg := bypassConfig()
	store := NewMemoryStore(cfg)
	_, _, _ = store.RecordTerminalFailure(context.Background(), "openai:gpt-4o")

	innerCalled := false
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		innerCalled = true
		// Query-param markers MUST be stripped from the URL before
		// forwarding upstream so provider logs don't show them.
		if req.URL.Query().Has(BypassQueryParam) {
			t.Error("bypass query param should be stripped before upstream RoundTrip")
		}
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := requestForModel("gpt-4o")
	q := req.URL.Query()
	q.Set(BypassQueryParam, "1")
	q.Set(BypassQueryParamReason, "manual_debug")
	req.URL.RawQuery = q.Encode()

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("query-param bypass must invoke inner transport on Open circuit")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 from upstream, got %d", resp.StatusCode)
	}
}

// ─── Per-model keying ────────────────────────────────────────────────────

// Per-model keying isolates the blast radius of a single misbehaving
// model: opening the breaker for one model must NOT prevent traffic to
// another model on the same provider.
func TestPerModelKeying_OneModelOpenDoesNotAffectAnother(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	store := NewMemoryStore(cfg)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// flash-lite is broken; flash-standard is fine.
		if strings.Contains(string(modelFromBody(req)), "flash-lite") {
			return makeResp(503), nil
		}
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	// Trip the flash-lite breaker.
	resp, err := tr.RoundTrip(requestForModel("flash-lite"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected synthetic 503 after flash-lite tripped, got %d", resp.StatusCode)
	}

	// flash-lite key must be Open.
	if state, _ := store.GetState(context.Background(), "openai:flash-lite"); state != StateOpen {
		t.Fatalf("openai:flash-lite breaker should be Open, got %s", state)
	}

	// flash-standard still works — its key is independent.
	resp, err = tr.RoundTrip(requestForModel("flash-standard"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("openai:flash-standard must still pass through; got %d", resp.StatusCode)
	}
	if state, _ := store.GetState(context.Background(), "openai:flash-standard"); state != StateClosed {
		t.Fatalf("openai:flash-standard breaker should be Closed, got %s", state)
	}
}

// Regression test for the fast-fail-after-cacheBody bug: once a
// per-model breaker is Open, the very next request for that model MUST
// be fast-failed without invoking the inner transport — even when the
// incoming request has req.GetBody == nil (the production case for
// server-side requests proxied via httputil.ReverseProxy).
//
// Pre-fix the state check happened in RoundTrip before cacheBody, so
// it only saw the bare-provider key (because keyFor falls back to the
// provider when GetBody is nil).  The bare-provider key is Closed in
// per-model mode → request enters the retry loop → upstream gets hit
// → handleTerminalFailure eventually returns synthetic 503.  The user
// sees a 503 either way, but the proxy keeps hammering the degraded
// upstream — defeating the entire point of fast-fail.
func TestPerModelKeying_OpenBreakerFastFailsWithoutUpstreamCall(t *testing.T) {
	cfg := bypassConfig()
	store := NewMemoryStore(cfg)
	// Pre-open the per-model breaker so the test does not depend on
	// failing-upstream + retry exhaustion to trip it.
	if _, openedNow, err := store.RecordTerminalFailure(context.Background(), "openai:gpt-4o"); err != nil {
		t.Fatalf("RecordTerminalFailure: %v", err)
	} else if !openedNow {
		t.Fatalf("expected first RecordTerminalFailure with FailureThreshold=1 to open the breaker")
	}

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	// Strip GetBody to simulate what http.Server hands the reverse
	// proxy: the body is readable once but not replayable, so the
	// model extractor cannot run until cacheBody buffers it.  This is
	// the configuration where the original bug actually fired.
	req := requestForModel("gpt-4o")
	req.GetBody = nil

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if innerCalled {
		t.Fatal("per-model breaker is Open — inner transport must NOT be called (fast-fail expected)")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want fast-fail 503, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("fast-fail body should carry DegradedSignal, got %s", b)
	}

	// And of course a different model on the same provider is unaffected,
	// also via the GetBody==nil path.
	innerCalled = false
	req2 := requestForModel("gpt-4o-mini")
	req2.GetBody = nil
	resp2, err := tr.RoundTrip(req2)
	if err != nil {
		t.Fatalf("unexpected error on healthy model: %v", err)
	}
	if !innerCalled {
		t.Fatal("healthy per-model key must reach the inner transport")
	}
	if resp2.StatusCode != 200 {
		t.Fatalf("want 200 from healthy key, got %d", resp2.StatusCode)
	}
}

// When the model extractor cannot identify a model (returns "") the
// transport must fall back to per-provider keying so we never silently
// lose breaker coverage on unattributable traffic.
func TestPerModelKeying_UnextractableModelIsolatedFromBareProvider(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	store := NewMemoryStore(cfg)

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(func(_ *http.Request) string { return "" }),
	)

	body := `{"messages":[]}` // no model field
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")

	if _, err := tr.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Failures from unextractable-model requests must open ONLY the dedicated
	// sentinel breaker, never the bare-provider key. Opening the bare provider
	// would fast-fail every healthy per-model request (a cheap DoS), since the
	// bare-provider state is joined into all per-model effective states and is
	// also the forced-open (insufficient_quota) overlay key.
	if state, _ := store.GetState(context.Background(), "openai:_unknown"); state != StateOpen {
		t.Fatalf("unextractable-model breaker should Open on its sentinel key, got %s", state)
	}
	if state, _ := store.GetState(context.Background(), "openai"); state != StateClosed {
		t.Fatalf("bare-provider breaker must remain Closed; unextractable failures must not contaminate it, got %s", state)
	}
}

// ─── Per-provider rollup ─────────────────────────────────────────────────

// When N distinct per-key breakers open within the rollup window, ALL
// keys for that provider — including ones whose individual breaker is
// still Closed — fast-fail.  This is the wholesale-outage signal.
func TestRollup_TripsAfterMultipleKeysOpen_FastFailsHealthyKey(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	cfg.PerProviderRollupThreshold = 2
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	// Pre-open two distinct per-model breakers and feed the rollup
	// signal directly so the test does not depend on the retry loop's
	// timing.  This mirrors what the transport does in handleTerminalFailure.
	for _, k := range []string{"openai:m1", "openai:m2"} {
		_, openedNow, err := store.RecordTerminalFailure(context.Background(), k)
		if err != nil {
			t.Fatalf("RecordTerminalFailure(%s): %v", k, err)
		}
		if !openedNow {
			t.Fatalf("expected RecordTerminalFailure(%s) to open the breaker on first hit", k)
		}
		if err := store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds); err != nil {
			t.Fatalf("RecordKeyOpenedForRollup(%s): %v", k, err)
		}
	}

	open, count, err := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
	if err != nil {
		t.Fatalf("RollupOpen: %v", err)
	}
	if !open || count < cfg.PerProviderRollupThreshold {
		t.Fatalf("rollup should be open after threshold reached; open=%v count=%d", open, count)
	}

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	// A request for an entirely different (still-Closed) per-model key
	// must be fast-failed by the rollup signal.
	resp, err := tr.RoundTrip(requestForModel("m3-still-healthy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if innerCalled {
		t.Fatal("rollup must fast-fail the request without invoking inner transport")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want fast-fail 503 from rollup, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), DefaultDegradedSignal) {
		t.Fatalf("rollup fast-fail body should carry DegradedSignal, got %s", body)
	}
}

// Threshold == 0 (default) keeps the rollup feature disabled — the
// transport behaves exactly as if the rollup did not exist.  This is
// the v1 keying behaviour and protects rollouts that have not opted in.
func TestRollup_DisabledByDefault(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	// Leave PerProviderRollupThreshold at its zero (disabled) default.
	store := NewMemoryStore(cfg)

	// Pre-open three distinct keys + record rollup events; even with a
	// (would-be) overflowing rollup count, the disabled feature must
	// not affect routing.
	for _, k := range []string{"openai:m1", "openai:m2", "openai:m3"} {
		_, _, _ = store.RecordTerminalFailure(context.Background(), k)
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", k, 60)
	}

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	resp, err := tr.RoundTrip(requestForModel("m4-healthy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatalf("disabled rollup must not gate traffic; inner not called")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 with rollup disabled, got %d", resp.StatusCode)
	}
}

// Recording the same key twice into the rollup window must NOT inflate
// the count: the rollup is a "currently degraded keys" set, not a "key
// open events ever" log.  Without dedup-by-key, a single flapping
// model could single-handedly trip the wholesale-degradation signal.
func TestRollup_RecordKeyOpenedForRollup_DedupesByKey(t *testing.T) {
	cfg := bypassConfig()
	cfg.PerProviderRollupThreshold = 3
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	for i := 0; i < 5; i++ {
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", "openai:m1", cfg.PerProviderRollupWindowSeconds)
	}
	open, count, _ := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
	if open {
		t.Fatalf("a single flapping key must not trip the rollup; open=%v count=%d", open, count)
	}
	if count != 1 {
		t.Fatalf("dedup-by-key should yield count=1 after 5 records of the same key, got %d", count)
	}
}

// On probe failure the transport re-arms the rollup window for that
// key so a long-burn outage (same N keys continuously down for hours)
// keeps tripping the rollup instead of silently aging out after the
// first window expires.  We exercise this by manually forcing the per-
// key state through Closed → Open → HalfOpen → probe-fail and asserting
// the rollup entry is refreshed (not duplicated, not lost).
func TestRollup_ReArmsOnProbeFailure(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	cfg.CooldownSeconds = 0 // force an immediate Open → HalfOpen transition
	cfg.PerProviderRollupThreshold = 2
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	// Open the per-model breaker (this also seeds the rollup entry).
	if _, err := tr.RoundTrip(requestForModel("flaky")); err != nil {
		t.Fatalf("first roundtrip: %v", err)
	}
	if state, _ := store.GetState(context.Background(), "openai:flaky"); state != StateOpen {
		t.Fatalf("breaker should be Open after first failure, got %s", state)
	}

	// Cooldown=0 means the next GetState observes HalfOpen → probe slot
	// is acquired and the upstream call (still failing) re-opens via
	// RecordProbeFailed → reArmRollup.
	if _, err := tr.RoundTrip(requestForModel("flaky")); err != nil {
		t.Fatalf("second roundtrip: %v", err)
	}

	// Assert: still exactly one entry for openai:flaky in the rollup
	// (dedup-by-key) AND the rollup is not yet at threshold.
	keys, err := store.RolledUpKeys(context.Background(), "openai", cfg.PerProviderRollupWindowSeconds)
	if err != nil {
		t.Fatalf("RolledUpKeys: %v", err)
	}
	if len(keys) != 1 || keys[0] != "openai:flaky" {
		t.Fatalf("rollup should hold exactly [openai:flaky] after probe-fail re-arm, got %v", keys)
	}
	open, count, _ := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
	if open {
		t.Fatalf("rollup should still be Closed (1 < threshold=2); open=%v count=%d", open, count)
	}
}

// Rollup-open must not starve per-model HalfOpen probes. Incoming
// server-side requests have GetBody == nil before cacheBody runs, so an
// eager pre-cacheBody rollup check can only see the bare provider key.
// If it fast-fails immediately, the transport never discovers that the
// actual per-model breaker is HalfOpen, which means successful probes
// can never clear keys from the rollup window.
func TestRollup_OpenProviderStillAllowsPerModelHalfOpenProbe_ForServerSideRequest(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1
	cfg.PerProviderRollupThreshold = 2
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	// Seed an open rollup with two currently degraded model keys.
	for _, k := range []string{"openai:flaky", "openai:other"} {
		_, _, _ = store.RecordTerminalFailure(context.Background(), k)
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds)
	}
	if open, count, _ := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds); !open || count != 2 {
		t.Fatalf("seed: expected rollup open with count=2, got open=%v count=%d", open, count)
	}

	// Move one key into HalfOpen, as if its cooldown expired.
	e := store.entry("openai:flaky")
	e.mu.Lock()
	e.state = StateHalfOpen
	e.cooldownUntil = time.Time{}
	e.mu.Unlock()

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := requestForModelServerSide("flaky")
	if req.GetBody != nil {
		t.Fatal("test setup invariant: GetBody must be nil so the pre-cacheBody rollup check cannot know the per-model key")
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("per-model HalfOpen probe must be allowed through even while provider rollup is open")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want successful probe 200, got %d", resp.StatusCode)
	}

	state, _ := store.GetState(context.Background(), "openai:flaky")
	if state != StateClosed {
		t.Fatalf("successful probe should close per-model breaker, got %s", state)
	}
	keys, err := store.RolledUpKeys(context.Background(), "openai", cfg.PerProviderRollupWindowSeconds)
	if err != nil {
		t.Fatalf("RolledUpKeys: %v", err)
	}
	for _, key := range keys {
		if key == "openai:flaky" {
			t.Fatalf("successful probe should clear openai:flaky from rollup keys, got %v", keys)
		}
	}
}

// /health-style sanity check: RolledUpKeys returns the live set after
// pruning aged-out events, in any order.
func TestRollup_RolledUpKeys_ReturnsLiveSnapshot(t *testing.T) {
	cfg := bypassConfig()
	cfg.PerProviderRollupThreshold = 3
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	for _, k := range []string{"openai:a", "openai:b", "openai:c"} {
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds)
	}
	keys, err := store.RolledUpKeys(context.Background(), "openai", cfg.PerProviderRollupWindowSeconds)
	if err != nil {
		t.Fatalf("RolledUpKeys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 keys in rollup, got %d (%v)", len(keys), keys)
	}
	got := map[string]bool{}
	for _, k := range keys {
		got[k] = true
	}
	for _, want := range []string{"openai:a", "openai:b", "openai:c"} {
		if !got[want] {
			t.Fatalf("expected %q in rollup snapshot, got %v", want, keys)
		}
	}

	if err := store.ClearRollupKey(context.Background(), "openai", "openai:b"); err != nil {
		t.Fatalf("ClearRollupKey: %v", err)
	}
	keys, _ = store.RolledUpKeys(context.Background(), "openai", cfg.PerProviderRollupWindowSeconds)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys after clearing one, got %d (%v)", len(keys), keys)
	}
	for _, k := range keys {
		if k == "openai:b" {
			t.Fatalf("openai:b should have been removed, but is still in %v", keys)
		}
	}
}

// In log mode the rollup is observed but never enforces.  This
// regression-checks two things at once:
//  1. The observe-only path emits a `would_have_fast_failed` log line
//     AND a `circuit.would_have_fast_failed` metric when the rollup is
//     open (so log → enforce promotion is risk-free).
//  2. The inner transport is STILL called — log mode never blocks
//     traffic regardless of rollup state.
func TestRollup_LogMode_DoesNotBlockButObserves(t *testing.T) {
	cfg := bypassConfig()
	cfg.Mode = ModeLog
	cfg.PerProviderRollupThreshold = 2
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)
	for _, k := range []string{"openai:m1", "openai:m2"} {
		_, _, _ = store.RecordTerminalFailure(context.Background(), k)
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds)
	}

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	logBuf := &bytes.Buffer{}
	metrics := &fakeMetrics{}
	tr := NewTransport(
		inner, store, cfg, "openai", captureLogs(logBuf),
		WithModelExtractor(modelFromBody),
		WithMetrics(metrics),
	)

	resp, err := tr.RoundTrip(requestForModel("m3-still-healthy"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("log mode must never block traffic, even when rollup is open")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("log mode should pass through real upstream response; got %d", resp.StatusCode)
	}

	// Counterfactual log line: the rollup is open, so observe-only mode
	// must emit `would_have_fast_failed (rollup open, passing through)`.
	if entry := findLogLine(t, logBuf, "would_have_fast_failed"); entry == nil {
		t.Fatalf("expected a would_have_fast_failed log line, got: %s", logBuf.String())
	}

	// Counterfactual metric: a single circuit.would_have_fast_failed
	// counter must be emitted with the provider tag set.
	wf := metrics.findByName("circuit.would_have_fast_failed")
	if len(wf) != 1 {
		t.Fatalf("expected exactly 1 circuit.would_have_fast_failed metric, got %d (%+v)", len(wf), metrics.calls)
	}
	if _, ok := tagSet(wf[0].Tags)["provider:openai"]; !ok {
		t.Fatalf("missing provider:openai tag on circuit.would_have_fast_failed (tags=%v)", wf[0].Tags)
	}
}

// Bypass-reason normalisation guards Datadog tag cardinality.  Verifies:
//   - empty reason → BypassReasonUnspecified
//   - mixed-case + special characters → lowercased + safe-charactered
//   - allowlist hit → reason emitted verbatim
//   - allowlist miss → BypassReasonOther
func TestNormalizeBypassReason(t *testing.T) {
	type tc struct {
		name      string
		raw       string
		allowlist []string
		want      string
	}

	cases := []tc{
		{"empty", "", nil, BypassReasonUnspecified},
		{"whitespace", "   ", nil, BypassReasonUnspecified},
		{"plain", "manual_debug", nil, "manual_debug"},
		{"casing+spaces", "  Manual Debug  ", nil, "manual_debug"},
		{"strips unsafe chars", "<script>alert(1)</script>", nil, "script_alert_1_script"},
		{"hyphen ok", "no-fallback", nil, "no-fallback"},
		{"allowlist hit", "manual_debug", []string{"manual_debug", "no_fallback_configured"}, "manual_debug"},
		{"allowlist miss", "rogue_value", []string{"manual_debug"}, BypassReasonOther},
		{"allowlist hit case-insensitive", "Manual_Debug", []string{"manual_debug"}, "manual_debug"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := bypassConfig()
			cfg.BypassReasonAllowlist = c.allowlist
			tr := NewTransport(roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return makeResp(200), nil
			}), NewMemoryStore(cfg), cfg, "openai", nil)

			got := tr.normalizeBypassReason(c.raw)
			if got != c.want {
				t.Fatalf("normalizeBypassReason(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// requestForModelServerSide builds a request with the body wrapped in
// an opaque io.Reader so http.NewRequest cannot infer a Length-aware
// type and therefore leaves req.GetBody == nil — exactly mirroring the
// shape of incoming requests that arrive through httputil.ReverseProxy
// (which never populate GetBody).  Tests that exercise the
// pre-cacheBody key-resolution paths MUST use this builder; the regular
// requestForModel uses bytes.NewReader which Go's stdlib special-cases
// to populate GetBody, hiding the bugs we're trying to regress against.
func requestForModelServerSide(model string) *http.Request {
	body := `{"model":"` + model + `","messages":[]}`
	req, _ := http.NewRequest(http.MethodPost, "/openai/v1/chat/completions",
		struct{ io.Reader }{Reader: bytes.NewReader([]byte(body))})
	req.Header.Set("Content-Type", "application/json")
	return req
}

// emptyModelServerSideRequest builds a server-side request (GetBody == nil)
// whose JSON body has no `model` field, so modelFromBody returns "" and
// keyFor falls back to the bare-provider key.  Used to regress the
// bare-provider fast-fail edge case.
func emptyModelServerSideRequest() *http.Request {
	body := `{"messages":[],"temperature":0.5}`
	req, _ := http.NewRequest(http.MethodPost, "/openai/v1/chat/completions",
		struct{ io.Reader }{Reader: bytes.NewReader([]byte(body))})
	req.Header.Set("Content-Type", "application/json")
	return req
}

// Bug 1 regression: runBypass must call cacheBody so the model extractor
// can run on incoming server-side requests (GetBody == nil).  Without
// the fix, every bypass failure was silently credited to the
// bare-provider key, causing the per-model breaker for gpt-4o to never
// learn from bypass-observed failures and the bare-provider breaker to
// fill up with cross-model noise.
//
// We verify the fix end-to-end: a single bypass call against an
// upstream returning 503 must record the failure under "openai:gpt-4o"
// (not "openai") so the per-model dashboard / breaker stay accurate.
func TestBypass_RecordsFailureUnderPerModelKey_ForServerSideRequest(t *testing.T) {
	cfg := bypassConfig()
	cfg.FailureThreshold = 1 // first 5xx must trip the per-model key
	store := NewMemoryStore(cfg)

	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Sanity: bypass markers stripped before upstream call.
		if req.Header.Get(BypassHeader) != "" {
			t.Errorf("bypass header leaked upstream: %q", req.Header.Get(BypassHeader))
		}
		return makeResp(503), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := requestForModelServerSide("gpt-4o")
	if req.GetBody != nil {
		t.Fatal("test setup invariant: GetBody must be nil so the test exercises cacheBody-in-runBypass")
	}
	req.Header.Set(BypassHeader, "1")

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("bypass must return real upstream response, got %d", resp.StatusCode)
	}

	// Per-model breaker MUST be Open after the bypass-observed failure.
	perModel, err := store.GetState(context.Background(), "openai:gpt-4o")
	if err != nil {
		t.Fatalf("GetState(openai:gpt-4o): %v", err)
	}
	if perModel != StateOpen {
		t.Fatalf("bypass failure must trip per-model breaker; got openai:gpt-4o=%s", perModel)
	}

	// Bare-provider breaker MUST stay Closed — failures from one model
	// shouldn't pollute the bare-provider key, even when surfaced via bypass.
	bare, err := store.GetState(context.Background(), "openai")
	if err != nil {
		t.Fatalf("GetState(openai): %v", err)
	}
	if bare != StateClosed {
		t.Fatalf("bare-provider breaker must stay Closed when bypass failure has an extractable model; got openai=%s", bare)
	}
}

// Bug 2 regression: when the bare-provider breaker is Open AND a
// request arrives whose model cannot be extracted (no model field, or
// oversize body), the post-cacheBody re-check must fast-fail.
//
// The original code wrapped the post-cacheBody re-check in
// `if key != t.provider`, which meant requests that fell back to the
// bare-provider key were never re-checked — they would consume the
// full retry budget against an upstream we already know is degraded,
// defeating the whole point of fast-fail for that key.
func TestPerModelKeying_BareProviderFastFail_ForUnextractableModel(t *testing.T) {
	cfg := bypassConfig()
	cfg.BypassAllowed = false // bypass is irrelevant here
	cfg.FailureThreshold = 1
	store := NewMemoryStore(cfg)
	// Pre-open the bare-provider breaker.
	if _, _, err := store.RecordTerminalFailure(context.Background(), "openai"); err != nil {
		t.Fatalf("RecordTerminalFailure(openai): %v", err)
	}

	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
	)

	req := emptyModelServerSideRequest()
	if req.GetBody != nil {
		t.Fatal("test setup invariant: GetBody must be nil to force the post-cacheBody key to be the bare-provider key")
	}

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if innerCalled {
		t.Fatal("post-cacheBody re-check must fast-fail when the bare-provider breaker is Open and the model cannot be extracted")
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want synthetic 503 fast-fail, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), DefaultDegradedSignal) {
		t.Fatalf("fast-fail body must contain DegradedSignal; got %q", body)
	}
}

// Bug 4 regression: a request with X-LLM-Proxy-Bypass-Circuit set to a
// FALSY value ("0") must (a) still route through the normal breaker
// path (no bypass effect), and (b) NOT leak the bypass markers to the
// upstream provider.  Before the fix, falsy values caused
// `bypassRequested` to return ok=false, but the headers were never
// stripped so they reached the provider's request log.
func TestBypass_FalsyValue_DoesNotLeakHeadersUpstream(t *testing.T) {
	cfg := bypassConfig()
	store := NewMemoryStore(cfg)

	innerCalled := false
	var sawHeaders, sawQuery bool
	inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		innerCalled = true
		if req.Header.Get(BypassHeader) != "" || req.Header.Get(BypassReasonHeader) != "" {
			sawHeaders = true
		}
		if req.URL != nil {
			q := req.URL.Query()
			if q.Has(BypassQueryParam) || q.Has(BypassQueryParamReason) {
				sawQuery = true
			}
		}
		return makeResp(200), nil
	})
	metrics := &fakeMetrics{}
	tr := NewTransport(
		inner, store, cfg, "openai", nil,
		WithModelExtractor(modelFromBody),
		WithMetrics(metrics),
	)

	req := requestForModel("gpt-4o")
	req.Header.Set(BypassHeader, "0") // falsy — must NOT trigger bypass
	req.Header.Set(BypassReasonHeader, "manual_debug")
	q := req.URL.Query()
	q.Set(BypassQueryParam, "0")
	q.Set(BypassQueryParamReason, "manual_debug")
	req.URL.RawQuery = q.Encode()

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("falsy bypass value must still route through the normal breaker path")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 from upstream, got %d", resp.StatusCode)
	}
	if sawHeaders {
		t.Fatal("bypass headers must be stripped before upstream RoundTrip even when the value is falsy")
	}
	if sawQuery {
		t.Fatal("bypass query params must be stripped before upstream RoundTrip even when the value is falsy")
	}

	// No `circuit.bypass` counter must be emitted: a falsy value means
	// bypass was never actually requested, so the bypass-accounting
	// metric (used for dashboards and alerting on unexpected bypass
	// volume) must stay at zero.  This guards the second half of the
	// Bug 4 fix — header stripping alone is not enough; the metric
	// path must short-circuit on falsy values too.
	if got := metrics.findByName("circuit.bypass"); len(got) != 0 {
		t.Fatalf("circuit.bypass must NOT be emitted for falsy bypass values; got %d calls (%+v)", len(got), got)
	}
}

// ClearRollupKey drops a per-key entry from the rollup window so a
// successful half-open probe immediately reduces the rollup count
// rather than waiting for the rollup window itself to age out.
func TestRollup_ClearRollupKey_RemovesEntryFromWindow(t *testing.T) {
	cfg := bypassConfig()
	cfg.PerProviderRollupThreshold = 2
	cfg.PerProviderRollupWindowSeconds = 60
	store := NewMemoryStore(cfg)

	// Seed two open events.
	for _, k := range []string{"openai:m1", "openai:m2"} {
		_ = store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds)
	}
	open, count, _ := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
	if !open || count != 2 {
		t.Fatalf("seed: rollup should be open with count=2; open=%v count=%d", open, count)
	}

	// Recovering one key drops the count below threshold.
	if err := store.ClearRollupKey(context.Background(), "openai", "openai:m1"); err != nil {
		t.Fatalf("ClearRollupKey: %v", err)
	}
	open, count, _ = store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
	if open {
		t.Fatalf("rollup should close after clearing one key; open=%v count=%d", open, count)
	}
	if count != 1 {
		t.Fatalf("count after ClearRollupKey: want 1, got %d", count)
	}
}

// Regression: bypass must short-circuit the rollup gate the same way it
// short-circuits the per-key gate.  Without this guarantee, a wholesale
// provider rollup would silently break the safety valve — a caller that
// has wired up bypass *because they have no fallback* would still get a
// synthetic 503, which is precisely the failure mode bypass exists to
// prevent.  The test exercises both forms (header AND query param) so
// neither code path can regress in isolation.
func TestRollup_OpenWithBypassHeader_StillCallsInnerTransport(t *testing.T) {
	subtests := []struct {
		name      string
		applyMark func(req *http.Request)
	}{
		{
			name: "header",
			applyMark: func(req *http.Request) {
				req.Header.Set(BypassHeader, "1")
				req.Header.Set(BypassReasonHeader, "no_fallback_configured")
			},
		},
		{
			name: "query_param",
			applyMark: func(req *http.Request) {
				q := req.URL.Query()
				q.Set(BypassQueryParam, "1")
				q.Set(BypassQueryParamReason, "no_fallback_configured")
				req.URL.RawQuery = q.Encode()
			},
		},
	}

	for _, tc := range subtests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := bypassConfig()
			cfg.FailureThreshold = 1
			cfg.PerProviderRollupThreshold = 2
			cfg.PerProviderRollupWindowSeconds = 60
			store := NewMemoryStore(cfg)

			// Open the rollup with two distinct degraded model keys so any
			// non-bypass request to the same provider would be fast-failed.
			for _, k := range []string{"openai:m1", "openai:m2"} {
				if _, _, err := store.RecordTerminalFailure(context.Background(), k); err != nil {
					t.Fatalf("RecordTerminalFailure(%s): %v", k, err)
				}
				if err := store.RecordKeyOpenedForRollup(context.Background(), "openai", k, cfg.PerProviderRollupWindowSeconds); err != nil {
					t.Fatalf("RecordKeyOpenedForRollup(%s): %v", k, err)
				}
			}
			open, count, err := store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
			if err != nil {
				t.Fatalf("RollupOpen: %v", err)
			}
			if !open || count < cfg.PerProviderRollupThreshold {
				t.Fatalf("seed: rollup must be Open before bypass attempt; open=%v count=%d", open, count)
			}

			innerCalled := false
			inner := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				innerCalled = true
				if got := req.Header.Get(BypassHeader); got != "" {
					t.Errorf("bypass header leaked upstream: %q", got)
				}
				if got := req.Header.Get(BypassReasonHeader); got != "" {
					t.Errorf("bypass reason header leaked upstream: %q", got)
				}
				if req.URL != nil {
					q := req.URL.Query()
					if q.Has(BypassQueryParam) {
						t.Errorf("bypass query param leaked upstream: %q", q.Get(BypassQueryParam))
					}
					if q.Has(BypassQueryParamReason) {
						t.Errorf("bypass reason query param leaked upstream: %q", q.Get(BypassQueryParamReason))
					}
				}
				return makeResp(200), nil
			})
			tr := NewTransport(
				inner, store, cfg, "openai", nil,
				WithModelExtractor(modelFromBody),
			)

			// Pick a per-model key whose own breaker is Closed so the only
			// thing that could fast-fail this request is the rollup gate.
			req := requestForModel("m3-still-healthy")
			tc.applyMark(req)

			resp, err := tr.RoundTrip(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !innerCalled {
				t.Fatalf("bypass must short-circuit provider rollup; inner transport was not invoked")
			}
			if resp.StatusCode != 200 {
				t.Fatalf("want real upstream 200 (bypass returns whatever upstream answered), got %d", resp.StatusCode)
			}
			body, _ := io.ReadAll(resp.Body)
			if strings.Contains(string(body), DefaultDegradedSignal) {
				t.Fatalf("bypass response must not be the synthetic degraded payload; got %q", body)
			}

			// The rollup itself must remain Open — bypass observed a
			// success but the breaker only closes on a real half-open
			// probe, so wholesale enforcement should still be tripped
			// for non-bypass traffic.
			open, _, _ = store.RollupOpen(context.Background(), "openai", cfg.PerProviderRollupThreshold, cfg.PerProviderRollupWindowSeconds)
			if !open {
				t.Fatalf("a successful bypass call must NOT close the rollup; rollup is now reporting closed")
			}
		})
	}
}
