package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/alicebob/miniredis/v2"
)

const testAgentLabel = "finch-agent-123"

type agentReserveCall struct {
	month, label string
	estimate     float64
	limitCents   int64
}

type agentAdjustCall struct {
	month, label string
	delta        float64
}

// fakeAgentSpend mimics the Redis reservation script: a reservation is
// refused once recorded spend plus outstanding reservations reach the cap.
type fakeAgentSpend struct {
	mu           sync.Mutex
	spendUSD     float64
	reservedUSD  float64
	reserveErr   error
	readDegraded bool
	unbound      bool
	adjustErr    error
	reserves     []agentReserveCall
	adjusts      []agentAdjustCall
}

func (f *fakeAgentSpend) UserMonthlySpendUSDDetailed(context.Context, string, string) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readDegraded {
		return 0, true
	}
	return f.spendUSD, false
}

func (f *fakeAgentSpend) ReserveUserMonthlySpend(_ context.Context, month, label string, estimateUSD float64, limitCents int64) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserves = append(f.reserves, agentReserveCall{month, label, estimateUSD, limitCents})
	if f.reserveErr != nil {
		return true, false, f.reserveErr
	}
	if f.unbound {
		return true, false, nil
	}
	if int64(math.Ceil((f.spendUSD+f.reservedUSD)*100)) >= limitCents {
		return false, true, nil
	}
	f.reservedUSD += estimateUSD
	return true, true, nil
}

func (f *fakeAgentSpend) AdjustUserMonthlyReservation(_ context.Context, month, label string, deltaUSD float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adjusts = append(f.adjusts, agentAdjustCall{month, label, deltaUSD})
	if f.adjustErr != nil {
		return f.adjustErr
	}
	f.reservedUSD = math.Max(0, f.reservedUSD+deltaUSD)
	return nil
}

func (f *fakeAgentSpend) snapshot() ([]agentReserveCall, []agentAdjustCall, float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentReserveCall(nil), f.reserves...), append([]agentAdjustCall(nil), f.adjusts...), f.reservedUSD
}

type fakeAgentLimits struct {
	overrides map[string]apikeys.EffectiveAgentLimits
	err       error
	cfg       config.AgentSpendLimitsConfig
}

func (f *fakeAgentLimits) Resolve(_ context.Context, label string) (apikeys.EffectiveAgentLimits, error) {
	if e, ok := f.overrides[label]; ok {
		return e, f.err
	}
	return apikeys.MergeAgentLimit(f.cfg, label, nil), f.err
}

type agentMetricCall struct {
	name string
	tags []string
}

type recordingAgentMetrics struct {
	mu    sync.Mutex
	calls []agentMetricCall
}

func (r *recordingAgentMetrics) Incr(name string, tags []string, _ float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, agentMetricCall{name, append([]string(nil), tags...)})
	return nil
}

func (r *recordingAgentMetrics) Distribution(string, float64, []string, float64) error { return nil }

func (r *recordingAgentMetrics) count(name string, wantTags ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c.name != name {
			continue
		}
		if containsAll(c.tags, wantTags) {
			n++
		}
	}
	return n
}

func (r *recordingAgentMetrics) all() []agentMetricCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agentMetricCall(nil), r.calls...)
}

func containsAll(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// testClock is a settable clock shared by the middleware and the test.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// syncBuffer guards log output written from the AfterFunc goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return buf
}

func agentSpendCfg(mode string) config.AgentSpendLimitsConfig {
	return config.AgentSpendLimitsConfig{
		Enabled:                 true,
		Mode:                    mode,
		UserPrefix:              "finch-agent-",
		DefaultSoftMonthlyCents: 10000,
		DefaultHardMonthlyCents: 20000,
		MaxHardMonthlyCents:     500000,
		LimitCacheTTLSeconds:    60,
	}
}

type agentSpendHarness struct {
	cfg     config.AgentSpendLimitsConfig
	spend   *fakeAgentSpend
	limits  *fakeAgentLimits
	metrics *recordingAgentMetrics
	clock   *testClock
	pm      *providers.ProviderManager
	grace   time.Duration
	est     CostEstimator
}

func newAgentSpendHarness(mode string) *agentSpendHarness {
	cfg := agentSpendCfg(mode)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})
	return &agentSpendHarness{
		cfg:     cfg,
		spend:   &fakeAgentSpend{},
		limits:  &fakeAgentLimits{cfg: cfg},
		metrics: &recordingAgentMetrics{},
		clock:   &testClock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)},
		pm:      pm,
		grace:   time.Hour,
		est:     flatEstimator,
	}
}

func (h *agentSpendHarness) options() AgentSpendLimitOptions {
	return AgentSpendLimitOptions{
		Config:           h.cfg,
		Limits:           h.limits,
		Spend:            h.spend,
		Estimate:         h.est,
		ReservationGrace: h.grace,
		Metrics:          h.metrics,
		Now:              h.clock.Now,
	}
}

// handler is the agent middleware behind MetaURLRewritingMiddleware, as in
// buildRouter.
func (h *agentSpendHarness) handler(next http.Handler) http.Handler {
	return MetaURLRewritingMiddleware(h.pm)(AgentSpendLimitMiddleware(h.pm, h.options())(next))
}

func (h *agentSpendHarness) do(req *http.Request, next http.Handler) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.handler(next).ServeHTTP(rr, req)
	return rr
}

func agentReq(path string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-4o-mini","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if called != nil {
			*called = true
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestAgentSpend_GovernedWhateverTheCredential(t *testing.T) {
	cases := map[string]func(r *http.Request) *http.Request{
		"no key": func(r *http.Request) *http.Request { return r },
		"bedrock-sidecar placeholder": func(r *http.Request) *http.Request {
			r.Header.Set("Authorization", "Bearer bedrock-sidecar")
			return r
		},
		"BYO key": func(r *http.Request) *http.Request {
			r.Header.Set("Authorization", "Bearer sk-proj-byo-provider-key-000000")
			return r
		},
		"iw key": func(r *http.Request) *http.Request {
			r.Header.Set("Authorization", "Bearer iw:abc123456789")
			key := &apikeys.APIKey{PK: "iw:abc123456789", MonthlyCostLimit: 1_000_000}
			return r.WithContext(apikeys.WithContext(r.Context(), key))
		},
	}
	for name, decorate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAgentSpendHarness(config.AgentSpendModeEnforce)
			h.spend.spendUSD = 200
			rr := h.do(decorate(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions")), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("next must not be called over the hard limit")
			}))
			require.Equal(t, http.StatusPaymentRequired, rr.Code)
			reserves, _, _ := h.spend.snapshot()
			require.Len(t, reserves, 1)
			assert.Equal(t, testAgentLabel, reserves[0].label)
		})
	}
}

func TestAgentSpend_EnforceBlockResponse(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.clock.Set(time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC))
	h.spend.spendUSD = 200.004
	logs := captureLog(t)

	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))

	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.JSONEq(t, `{"error":"[PROXY] monthly agent cost limit exceeded"}`, rr.Body.String())
	assert.Equal(t, "monthly_agent_cost_limit_exceeded", rr.Header().Get("X-Cost-Limit-Reason"))
	assert.Equal(t, "20000", rr.Header().Get("X-Cost-Limit-Cents"))
	assert.Equal(t, "20001", rr.Header().Get("X-Cost-Spend-Cents"))
	assert.Equal(t, "2027-01-01T00:00:00Z", rr.Header().Get("X-Agent-Spend-Reset"))
	assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:block", "mode:enforce", "label:"+testAgentLabel))
	assert.Contains(t, logs.String(), "agentspend: block")
	_, adjusts, _ := h.spend.snapshot()
	assert.Empty(t, adjusts, "a refused reservation has nothing to release")
}

func TestAgentSpend_ObserveNeverBlocks(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeObserve)
	h.spend.spendUSD = 500
	logs := captureLog(t)

	var called bool
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(&called))

	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
	assert.Empty(t, rr.Header().Get("X-Cost-Limit-Reason"))
	assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:would_block", "mode:observe", "label:"+testAgentLabel))
	assert.Contains(t, logs.String(), "would block (observe)")
	assert.Contains(t, logs.String(), "label="+testAgentLabel)
}

func TestAgentSpend_ObserveStillReservesAndReconciles(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeObserve)
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusOK, rr.Code)
	reserves, adjusts, _ := h.spend.snapshot()
	require.Len(t, reserves, 1)
	require.Len(t, adjusts, 1)
	assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:allow", "mode:observe"))
}

func TestAgentSpend_UngovernedLabelsPass(t *testing.T) {
	cases := map[string]func() *http.Request{
		"label without prefix": func() *http.Request {
			return agentReq("/meta/support-bot/openai/v1/chat/completions")
		},
		"prefix only": func() *http.Request {
			return agentReq("/meta/finch-agent-/openai/v1/chat/completions")
		},
		"no meta path or header": func() *http.Request {
			return agentReq("/openai/v1/chat/completions")
		},
		"token-prefix fallback": func() *http.Request {
			r := agentReq("/openai/v1/chat/completions")
			r.Header.Set("Authorization", "Bearer finch-agent-123456789")
			return r
		},
		"query parameter": func() *http.Request {
			return agentReq("/openai/v1/chat/completions?llm_user_id=" + testAgentLabel)
		},
		"ip fallback": func() *http.Request {
			r := agentReq("/openai/v1/chat/completions")
			r.Header.Set("X-Forwarded-For", testAgentLabel)
			return r
		},
		"meta label wins over X-User-ID": func() *http.Request {
			r := agentReq("/meta/support-bot/openai/v1/chat/completions")
			r.Header.Set("X-User-ID", testAgentLabel)
			return r
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			h := newAgentSpendHarness(config.AgentSpendModeEnforce)
			h.spend.spendUSD = 500
			var called bool
			rr := h.do(mk(), okHandler(&called))
			require.Equal(t, http.StatusOK, rr.Code)
			assert.True(t, called)
			reserves, _, _ := h.spend.snapshot()
			assert.Empty(t, reserves)
			assert.Empty(t, h.metrics.all())
		})
	}
}

func TestAgentSpend_XUserIDIsGoverned(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.spend.spendUSD = 200
	req := agentReq("/openai/v1/chat/completions")
	req.Header.Set("X-User-ID", testAgentLabel)
	rr := h.do(req, okHandler(nil))
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.Equal(t, testAgentLabel, ExtractUserIDFromRequest(req, nil), "label must match the cost tracker's attribution")
}

func TestAgentSpend_ExemptSkipsReservation(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.spend.spendUSD = 10_000
	h.limits.overrides = map[string]apikeys.EffectiveAgentLimits{
		testAgentLabel: {Label: testAgentLabel, SoftMonthlyCents: 10000, HardMonthlyCents: 20000, Exempt: true},
	}
	var called bool
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(&called))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, called)
	reserves, adjusts, _ := h.spend.snapshot()
	assert.Empty(t, reserves)
	assert.Empty(t, adjusts)
	assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:exempt"))
}

func TestAgentSpend_ZeroHardLimitBlocksEveryCall(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.limits.overrides = map[string]apikeys.EffectiveAgentLimits{
		testAgentLabel: {Label: testAgentLabel},
	}
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.Equal(t, "0", rr.Header().Get("X-Cost-Limit-Cents"))
}

func TestAgentSpend_StreamReconciledAfterStreamEnds(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.grace = 20 * time.Millisecond

	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: {}\n\n")
			w.(http.Flusher).Flush()
			_, adjusts, _ := h.spend.snapshot()
			if len(adjusts) != 0 {
				t.Errorf("reservation adjusted mid-stream: %+v", adjusts)
			}
		}
		// TokenParsingMiddleware sets these once the final usage chunk is parsed.
		w.Header().Set("X-LLM-Input-Tokens", "1000")
		w.Header().Set("X-LLM-Output-Tokens", "4000")
	})
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), next)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.True(t, rr.Flushed)

	reserves, adjusts, _ := h.spend.snapshot()
	require.Len(t, reserves, 1)
	require.Len(t, adjusts, 1)
	actual := 5000 * 1e-6
	assert.InDelta(t, actual-reserves[0].estimate, adjusts[0].delta, 1e-12)

	require.Eventually(t, func() bool {
		_, adjusts, reserved := h.spend.snapshot()
		return len(adjusts) == 2 && reserved == 0
	}, time.Second, 5*time.Millisecond, "reservation released after the grace period")
	_, adjusts, _ = h.spend.snapshot()
	assert.InDelta(t, -actual, adjusts[1].delta, 1e-12)
}

func TestAgentSpend_Upstream5xxReleasesReservation(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), next)
	require.Equal(t, http.StatusBadGateway, rr.Code)

	reserves, adjusts, reserved := h.spend.snapshot()
	require.Len(t, reserves, 1)
	require.Len(t, adjusts, 1)
	assert.InDelta(t, -reserves[0].estimate, adjusts[0].delta, 1e-12)
	assert.Zero(t, reserved)
}

func TestAgentSpend_PanicStillSettlesReservation(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic(http.ErrAbortHandler)
	})
	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), next)
	})
	_, adjusts, _ := h.spend.snapshot()
	require.Len(t, adjusts, 1, "estimate kept as the actual and reconciled")
	assert.Zero(t, adjusts[0].delta)
}

func TestAgentSpend_MonthFixedAtRequestStart(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.grace = 10 * time.Millisecond
	h.clock.Set(time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC))
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.clock.Advance(2 * time.Second)
		w.Header().Set("X-LLM-Input-Tokens", "10")
		w.Header().Set("X-LLM-Output-Tokens", "10")
		w.WriteHeader(http.StatusOK)
	})
	h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), next)
	require.Eventually(t, func() bool {
		_, adjusts, _ := h.spend.snapshot()
		return len(adjusts) == 2
	}, time.Second, 5*time.Millisecond)
	reserves, adjusts, _ := h.spend.snapshot()
	assert.Equal(t, "2026-11", reserves[0].month)
	for _, a := range adjusts {
		assert.Equal(t, "2026-11", a.month)
	}
}

func TestAgentSpend_UnpricedModelUsesRecordedSpend(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.est = func(string, string, int, int, int, int) float64 { return 0 }

	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusOK, rr.Code)

	h.spend.spendUSD = 200
	rr = h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.Equal(t, "20000", rr.Header().Get("X-Cost-Spend-Cents"))

	reserves, _, _ := h.spend.snapshot()
	assert.Empty(t, reserves)
}

func TestAgentSpend_DegradedFailsOpenInBothModes(t *testing.T) {
	type setup func(h *agentSpendHarness)
	cases := map[string]struct {
		setup  setup
		reason string
	}{
		"reserve error": {func(h *agentSpendHarness) { h.spend.reserveErr = errors.New("redis down") }, AgentSpendDegradedReserveError},
		"read error": {func(h *agentSpendHarness) {
			h.est = nil
			h.spend.readDegraded = true
		}, AgentSpendDegradedReadError},
		"limit lookup error": {func(h *agentSpendHarness) { h.limits.err = errors.New("dynamodb down") }, AgentSpendDegradedLimitLookupError},
		"store unbound":      {func(h *agentSpendHarness) { h.spend.unbound = true }, AgentSpendDegradedStoreUnbound},
	}
	for _, mode := range []string{config.AgentSpendModeObserve, config.AgentSpendModeEnforce} {
		for name, tc := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) {
				h := newAgentSpendHarness(mode)
				h.spend.spendUSD = 500 // would block if the check could run
				tc.setup(h)
				var called bool
				rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(&called))
				require.Equal(t, http.StatusOK, rr.Code)
				assert.True(t, called)
				assert.Equal(t, 1, h.metrics.count("agent_spend.enforcement_degraded", "reason:"+tc.reason, "mode:"+mode))
				assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:fail_open"))
				for _, c := range h.metrics.all() {
					if c.name == "agent_spend.enforcement_degraded" {
						for _, tag := range c.tags {
							assert.NotContains(t, tag, testAgentLabel)
						}
					}
				}
			})
		}
	}
}

func TestAgentSpend_FailClosedOnlyInEnforceMode(t *testing.T) {
	for _, mode := range []string{config.AgentSpendModeObserve, config.AgentSpendModeEnforce} {
		t.Run(mode, func(t *testing.T) {
			h := newAgentSpendHarness(mode)
			h.cfg.FailClosedOnReadError = true
			h.spend.reserveErr = errors.New("redis down")
			rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
			if mode == config.AgentSpendModeEnforce {
				require.Equal(t, http.StatusServiceUnavailable, rr.Code)
				assert.Equal(t, "monthly_agent_cost_limit_read_degraded", rr.Header().Get("X-Cost-Limit-Reason"))
				assert.Equal(t, 1, h.metrics.count("agent_spend.decision", "decision:fail_closed"))
			} else {
				require.Equal(t, http.StatusOK, rr.Code)
			}
			assert.Equal(t, 1, h.metrics.count("agent_spend.enforcement_degraded", "reason:reserve_error"))
		})
	}
}

func TestAgentSpend_ReconcileErrorIsDegraded(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.spend.adjustErr = errors.New("redis down")
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, 1, h.metrics.count("agent_spend.enforcement_degraded", "reason:reconcile_error"))
}

func TestAgentSpend_DegradedLogOncePerMinutePerReason(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.spend.reserveErr = errors.New("redis down: key llm:admin:cost:month:2026-11:by_user field " + testAgentLabel)
	logs := captureLog(t)
	handler := h.handler(okHandler(nil))
	serve := func(path string) {
		handler.ServeHTTP(httptest.NewRecorder(), agentReq(path))
	}

	for i := 0; i < 5; i++ {
		serve("/meta/" + testAgentLabel + "/openai/v1/chat/completions")
	}
	assert.Equal(t, 1, strings.Count(logs.String(), "reason=reserve_error"))
	assert.Contains(t, logs.String(), "agentspend: enforcement degraded, failing open reason=reserve_error count_since_last=1")
	assert.Equal(t, 5, h.metrics.count("agent_spend.enforcement_degraded", "reason:reserve_error"))

	h.clock.Advance(30 * time.Second)
	serve("/meta/" + testAgentLabel + "/openai/v1/chat/completions")
	assert.Equal(t, 1, strings.Count(logs.String(), "reason=reserve_error"))

	h.clock.Advance(31 * time.Second)
	serve("/meta/" + testAgentLabel + "/openai/v1/chat/completions")
	assert.Equal(t, 2, strings.Count(logs.String(), "reason=reserve_error"))
	assert.Contains(t, logs.String(), "reason=reserve_error count_since_last=6")

	assert.NotContains(t, logs.String(), testAgentLabel, "degraded log lines carry no label")
	assert.NotContains(t, logs.String(), "redis down", "degraded log lines carry no error text")
}

func TestAgentSpend_DegradedLogLimiterIsPerReason(t *testing.T) {
	clock := &testClock{t: time.Unix(0, 0)}
	l := newDegradedLogLimiter(time.Minute, clock.Now)
	_, ok := l.allow(AgentSpendDegradedReserveError)
	assert.True(t, ok)
	_, ok = l.allow(AgentSpendDegradedReserveError)
	assert.False(t, ok)
	n, ok := l.allow(AgentSpendDegradedLimitLookupError)
	assert.True(t, ok)
	assert.EqualValues(t, 1, n)
}

func TestNextMonthStartUTC(t *testing.T) {
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), nextMonthStartUTC(time.Date(2026, 12, 15, 8, 0, 0, 0, time.UTC)))
	pst := time.FixedZone("PST", -8*3600)
	assert.Equal(t, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), nextMonthStartUTC(time.Date(2026, 11, 30, 17, 0, 0, 0, pst)),
		"Nov 30 17:00 PST is Dec 1 UTC, so the reset is Jan 1")
}

func TestAgentSpendLimitMiddleware_DisabledIsPassThrough(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.cfg.Enabled = false
	h.spend.spendUSD = 500
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusOK, rr.Code)
	reserves, _, _ := h.spend.snapshot()
	assert.Empty(t, reserves)
}

// Nov 30 17:00 PST is already December in UTC, so the month and the reset
// follow UTC, not the server's zone.
func TestAgentSpend_ResetHeaderUsesUTCMonth(t *testing.T) {
	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.clock.Set(time.Date(2026, 11, 30, 17, 0, 0, 0, time.FixedZone("PST", -8*3600)))
	h.spend.spendUSD = 200
	rr := h.do(agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"), okHandler(nil))
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.Equal(t, "2027-01-01T00:00:00Z", rr.Header().Get("X-Agent-Spend-Reset"))
}

// realRecorderOptions points the middleware at a coststats.Recorder, as
// buildRouter does.
func realRecorderOptions(rec *coststats.Recorder, mode string, metrics *recordingAgentMetrics) AgentSpendLimitOptions {
	cfg := agentSpendCfg(mode)
	return AgentSpendLimitOptions{
		Config:           cfg,
		Limits:           &fakeAgentLimits{cfg: cfg},
		Spend:            rec,
		Estimate:         flatEstimator,
		ReservationGrace: time.Hour,
		Metrics:          metrics,
	}
}

func TestAgentSpend_RealRecorderReservesAndBlocks(t *testing.T) {
	store, err := adminrollup.NewStore(adminrollup.Config{Enabled: true, Backend: adminrollup.BackendMemory})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	rec := coststats.NewRecorder()
	rec.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
	rec.SetMonthlyUserPrefix("finch-agent-")

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})
	metrics := &recordingAgentMetrics{}
	handler := MetaURLRewritingMiddleware(pm)(AgentSpendLimitMiddleware(pm, realRecorderOptions(rec, config.AgentSpendModeEnforce, metrics))(okHandler(nil)))
	month := time.Now().UTC().Format("2006-01")
	ctx := context.Background()

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"))
	require.Equal(t, http.StatusOK, rr.Code)
	reserved, err := store.ReservedUserMonthlySpendUSD(ctx, adminrollup.MetricCost, month, testAgentLabel)
	require.NoError(t, err)
	assert.Greater(t, reserved, 0.0, "reservation lingers through the grace period")

	require.NoError(t, store.ApplyMonthlyUserSpend(ctx, adminrollup.MetricCost, month, testAgentLabel, 200))
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"))
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
	assert.Equal(t, "20000", rr.Header().Get("X-Cost-Spend-Cents"))
}

func TestAgentSpend_RedisDownFailsOpenInBothModes(t *testing.T) {
	for _, mode := range []string{config.AgentSpendModeObserve, config.AgentSpendModeEnforce} {
		for _, priced := range []bool{true, false} {
			reason := AgentSpendDegradedReserveError
			if !priced {
				reason = AgentSpendDegradedReadError
			}
			t.Run(mode+"/"+reason, func(t *testing.T) {
				mr, err := miniredis.Run()
				require.NoError(t, err)
				store, err := adminrollup.NewStore(adminrollup.Config{
					Enabled: true,
					Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
				})
				require.NoError(t, err)
				t.Cleanup(func() { _ = store.Close() })
				rec := coststats.NewRecorder()
				rec.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
				mr.Close()

				pm := providers.NewProviderManager()
				pm.RegisterProvider(&fakeProvider{})
				metrics := &recordingAgentMetrics{}
				opts := realRecorderOptions(rec, mode, metrics)
				if !priced {
					opts.Estimate = nil
				}
				var called bool
				handler := MetaURLRewritingMiddleware(pm)(AgentSpendLimitMiddleware(pm, opts)(okHandler(&called)))
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, agentReq("/meta/"+testAgentLabel+"/openai/v1/chat/completions"))

				require.Equal(t, http.StatusOK, rr.Code)
				assert.True(t, called)
				assert.Equal(t, 1, metrics.count("agent_spend.enforcement_degraded", "reason:"+reason))
			})
		}
	}
}

// The design marks the /meta/ rewrite for Bedrock Mantle paths unverified.
// This runs a SigV4-shaped Mantle request through the production order
// (memo, meta rewrite, agent spend) and checks the label resolves and that
// the body, path, and signed headers reach the provider unchanged.
func TestAgentSpend_BedrockMantleMetaPath(t *testing.T) {
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	mantle, err := providers.NewBedrockMantleProxy()
	require.NoError(t, err)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(mantle)

	body := `{"model":"openai.gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`
	signed := map[string]string{
		"Authorization":        "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261120/us-west-2/bedrock-mantle/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date, Signature=abc",
		"X-Amz-Date":           "20261120T120000Z",
		"X-Amz-Content-Sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"X-Amz-Security-Token": "token",
		"Content-Type":         "application/json",
	}
	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/meta/finch-agent-1/bedrock-mantle/v1/chat/completions", strings.NewReader(body))
		for k, v := range signed {
			r.Header.Set(k, v)
		}
		return r
	}

	type seen struct {
		path, rawPath, body, userID string
		headers                     http.Header
	}
	capture := func(out *seen) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			*out = seen{r.URL.Path, r.URL.RawPath, string(b), ExtractUserIDFromRequest(r, mantle), r.Header.Clone()}
			w.WriteHeader(http.StatusOK)
		})
	}
	chain := func(agent func(http.Handler) http.Handler, next http.Handler) http.Handler {
		return RequestMemoMiddleware()(MetaURLRewritingMiddleware(pm)(agent(next)))
	}

	var without seen
	chain(func(h http.Handler) http.Handler { return h }, capture(&without)).ServeHTTP(httptest.NewRecorder(), newReq())

	h := newAgentSpendHarness(config.AgentSpendModeEnforce)
	h.pm = pm
	var with seen
	rr := httptest.NewRecorder()
	chain(AgentSpendLimitMiddleware(pm, h.options()), capture(&with)).ServeHTTP(rr, newReq())

	require.Equal(t, http.StatusOK, rr.Code)
	reserves, _, _ := h.spend.snapshot()
	require.Len(t, reserves, 1, "the Mantle /meta/ label is governed")
	assert.Equal(t, "finch-agent-1", reserves[0].label)
	assert.Equal(t, "finch-agent-1", with.userID)
	assert.Equal(t, "/bedrock-mantle/v1/chat/completions", with.path)
	assert.Equal(t, without, with, "agent spend must not change what the provider signs")
	assert.Equal(t, body, with.body)
	for k, v := range signed {
		assert.Equal(t, v, with.headers.Get(k), k)
	}

	h.spend.spendUSD = 200
	rr = httptest.NewRecorder()
	chain(AgentSpendLimitMiddleware(pm, h.options()), capture(&with)).ServeHTTP(rr, newReq())
	require.Equal(t, http.StatusPaymentRequired, rr.Code)
}
