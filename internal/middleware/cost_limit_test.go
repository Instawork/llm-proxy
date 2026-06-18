package middleware

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestCostLimitMiddleware_AllowsUnderCap(t *testing.T) {
	rec := coststats.NewRecorder()
	masked := MaskKeyID("iw:abc123456789")
	rec.RecordRequest("openai", masked, "", "gpt-4o-mini", 0.0005, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCostLimitMiddleware_BlocksAtCap(t *testing.T) {
	rec := coststats.NewRecorder()
	masked := MaskKeyID("iw:abc123456789")
	rec.RecordRequest("openai", masked, "", "gpt-4o-mini", 1.0, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get(costLimitReasonHeader); got != costLimitExceeded {
		t.Fatalf("reason header = %q", got)
	}
}

func TestCostLimitMonthlyExceeded(t *testing.T) {
	reader := monthlySpendReader{monthlyUSD: 11.0}

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 0, MonthlyCostLimit: 1000}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, reader)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get(costLimitReasonHeader); got != costLimitMonthlyExceeded {
		t.Fatalf("reason header = %q want %q", got, costLimitMonthlyExceeded)
	}
}

type monthlySpendReader struct {
	dailyUSD   float64
	monthlyUSD float64
}

func (m monthlySpendReader) KeySpendUSD(context.Context, string) float64   { return m.dailyUSD }
func (m monthlySpendReader) KeyMonthlySpendUSD(context.Context, string) float64 { return m.monthlyUSD }

// Two distinct keys that share the first 12 characters must NOT share a spend
// bucket. Before MaskKeyID hashed the whole key, both collapsed to the same
// 12-char prefix, so one key's spend counted against the other's cap (breaking
// isolation / enabling cross-key DoS).
func TestCostLimitMiddleware_NoPrefixCollisionAcrossKeys(t *testing.T) {
	const pk1 = "iw:abcdefghijAAAAAAAAAAAAAAAAAAAA"
	const pk2 = "iw:abcdefghijBBBBBBBBBBBBBBBBBBBB"
	if pk1[:12] != pk2[:12] {
		t.Fatalf("test setup: keys must share a 12-char prefix")
	}
	if MaskKeyID(pk1) == MaskKeyID(pk2) {
		t.Fatalf("MaskKeyID collision: %q == %q", MaskKeyID(pk1), MaskKeyID(pk2))
	}

	rec := coststats.NewRecorder()
	// Burn well over the cap on key1.
	rec.RecordRequest("openai", MaskKeyID(pk1), "", "gpt-4o-mini", 5.0, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	// key2 has spent nothing and must be allowed despite the shared prefix.
	key2 := &apikeys.APIKey{PK: pk2, DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key2))
	rr := httptest.NewRecorder()
	called := false
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if !called || rr.Code != http.StatusOK {
		t.Fatalf("key2 wrongly blocked by key1's spend: code=%d called=%v", rr.Code, called)
	}
}

// Pins the masked-id contract the web UI mirrors (web/src/lib/format.ts).
// FNV-1a/32 of the full key, 8-char zero-padded lowercase hex, after the
// 12-char prefix + ellipsis. If this changes, the frontend join must change too.
func TestMaskKeyID_FormatContract(t *testing.T) {
	const pk = "iw:0123456789abcdefghijklmnop"
	got := MaskKeyID(pk)
	want := "iw:012345678" + "…" + keyIDHashSuffix(pk)
	if got != want {
		t.Fatalf("MaskKeyID = %q want %q", got, want)
	}
	suf := keyIDHashSuffix(pk)
	if len(suf) != 8 {
		t.Fatalf("hash suffix len = %d want 8 (%q)", len(suf), suf)
	}
	if keyIDHashSuffix(pk) != suf {
		t.Fatal("hash suffix not deterministic")
	}
	// Short keys are returned verbatim (no suffix) since the full value is shown.
	if MaskKeyID("iw:short") != "iw:short" {
		t.Fatalf("short key unexpectedly masked: %q", MaskKeyID("iw:short"))
	}
}

func TestCostLimitMiddleware_ZeroLimitUnlimited(t *testing.T) {
	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", "iw:abc123456789", "", "gpt-4o-mini", 99.0, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 0}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestCostLimitMiddleware_SkipsNonProviderRoutes(t *testing.T) {
	rec := coststats.NewRecorder()
	pm := providers.NewProviderManager()
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 1}
	rec.RecordRequest("openai", "iw:abc123456789", "", "", 99.0, 0, 0, 0, 0)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on /health, got %d", rr.Code)
	}
}

// degradedSpendReader reports a degraded fleet read (rollup bound, Redis down)
// while returning a local-only spend that is UNDER the cap. This models the
// dangerous window where other instances may have already pushed the fleet
// over the cap but this node cannot see it.
type degradedSpendReader struct{ localUSD float64 }

func (d degradedSpendReader) KeySpendUSD(context.Context, string) float64 { return d.localUSD }
func (d degradedSpendReader) KeySpendUSDDetailed(context.Context, string) (float64, bool) {
	return d.localUSD, true
}

func TestCostLimitMiddleware_DegradedFailOpenEnforcesLocal(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	// local spend (0.10 USD = 10c) is under the 100c cap → fail-open lets it through.
	reader := degradedSpendReader{localUSD: 0.10}
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	called := false
	CostLimitMiddleware(pm, reader)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if !called || rr.Code != http.StatusOK {
		t.Fatalf("fail-open should enforce local view and pass under-cap request; code=%d called=%v", rr.Code, called)
	}
}

func TestCostLimitMiddleware_DegradedFailClosedBlocks(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	// Same under-cap local view, but fail-closed must reject because fleet
	// visibility is lost and the cluster could already be over the cap.
	reader := degradedSpendReader{localUSD: 0.10}
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, reader, CostLimitOptions{FailClosedOnReadError: true})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next should not be called when failing closed")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("fail-closed should return 503, got %d", rr.Code)
	}
	if got := rr.Header().Get(costLimitReasonHeader); got != costLimitDegraded {
		t.Fatalf("reason header = %q want %q", got, costLimitDegraded)
	}
}

// fakeReserver implements both KeySpendReader and KeySpendReserver so the
// reservation enforcement path can be exercised without Redis.
type fakeReserver struct {
	mu          sync.Mutex
	allow       bool
	active      bool
	reservedEst float64
	adjustments []float64
}

func (f *fakeReserver) KeySpendUSD(context.Context, string) float64 { return 0 }
func (f *fakeReserver) ReserveKeySpend(_ context.Context, _ string, estimateUSD float64, _ int64) (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.active && f.allow {
		f.reservedEst = estimateUSD
	}
	return f.allow, f.active
}
func (f *fakeReserver) AdjustKeyReservation(_ context.Context, _ string, deltaUSD float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.adjustments = append(f.adjustments, deltaUSD)
}
func (f *fakeReserver) totalAdjust() float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sum float64
	for _, d := range f.adjustments {
		sum += d
	}
	return sum
}

func flatEstimator(_, _ string, inputTokens, outputTokens int) float64 {
	// 1 micro-dollar per token, deterministic and non-zero.
	return float64(inputTokens+outputTokens) * 1e-6
}

func TestCostLimitMiddleware_ReservationBlocksOverCap(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	reserver := &fakeReserver{allow: false, active: true} // cap reached
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, reserver, CostLimitOptions{Estimate: flatEstimator})(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next must not be called when reservation is blocked")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("blocked reservation should 402, got %d", rr.Code)
	}
}

func TestCostLimitMiddleware_ReservationAllowsAndReconciles(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	reserver := &fakeReserver{allow: true, active: true}
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100000}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	called := false
	CostLimitMiddleware(pm, reserver, CostLimitOptions{
		Estimate:         flatEstimator,
		ReservationGrace: 15 * time.Millisecond,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("X-LLM-Input-Tokens", "100")
		w.Header().Set("X-LLM-Output-Tokens", "50")
		w.Header().Set("X-LLM-Model", "gpt-4o-mini")
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if !called || rr.Code != http.StatusOK {
		t.Fatalf("allowed reservation should serve request; code=%d called=%v", rr.Code, called)
	}
	// Wait for the grace-period release to fire, then the net of all
	// reservation adjustments must equal -estimate (the up-front reservation
	// is fully unwound once the actual spend is recorded).
	time.Sleep(60 * time.Millisecond)
	if got := reserver.totalAdjust(); math.Abs(got+reserver.reservedEst) > 1e-9 {
		t.Fatalf("net reservation adjustments = %v, want -estimate (%v)", got, -reserver.reservedEst)
	}
}

func TestCostLimitMiddleware_ReservationInactiveFallsBackToRead(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	// active=false → no fleet reservation; middleware must fall back to the
	// read-only path (KeySpendUSD returns 0 here → under cap → allowed).
	reserver := &fakeReserver{allow: false, active: false}
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	called := false
	CostLimitMiddleware(pm, reserver, CostLimitOptions{Estimate: flatEstimator})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if !called || rr.Code != http.StatusOK {
		t.Fatalf("inactive reservation should fall back to read-only allow; code=%d called=%v", rr.Code, called)
	}
}
