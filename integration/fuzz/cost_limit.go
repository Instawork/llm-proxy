package fuzz

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/integration/live"
)

const (
	// One cent daily cap — enough for one large fake request at ~3¢ cost in tests
	// that intentionally overspend, then blocks follow-ups.
	fuzzCostLimitOneCent int64 = 1
	fuzzCostLimitHigh    int64 = 100_000 // $1000/day
)

func isCostLimitBlocked(res ChatResult) bool {
	if res.Status == http.StatusPaymentRequired {
		return true
	}
	return strings.Contains(strings.ToLower(res.Body), "daily cost limit")
}

func (r *Runner) costLimitZeroUnlimited(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-unlimited", 5000, 5_000_000, 0)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	for i := 0; i < 5; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 32})
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("request %d status %d", i, res.Status)
		}
	}
	return true, "zero daily_cost_limit allows traffic"
}

func (r *Runner) costLimitBlocksSecondRequest(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-block", 5000, 5_000_000, fuzzCostLimitOneCent)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	before, _ := CountLines(r.cfg.CostFile)
	first := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000})
	if first.Status != http.StatusOK {
		return false, fmt.Sprintf("first request want 200 got %d body=%s", first.Status, truncate(first.Body, 120))
	}
	if _, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1); err != nil {
		return false, "cost not recorded after first request: " + err.Error()
	}
	second := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4})
	if !isCostLimitBlocked(second) {
		return false, fmt.Sprintf("second request want 402 got %d body=%s", second.Status, truncate(second.Body, 120))
	}
	if second.Headers.Get("X-Cost-Limit-Reason") != "daily_cost_limit_exceeded" {
		return false, "missing X-Cost-Limit-Reason header"
	}
	return true, "blocked after daily cap reached"
}

func (r *Runner) costLimitAllowsUnderCap(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-under", 5000, 5_000_000, fuzzCostLimitHigh)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	for i := 0; i < 10; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 8})
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("request %d status %d", i, res.Status)
		}
	}
	return true, "high cap allows burst"
}

func (r *Runner) costLimitNoChargeWhenBlocked(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-no-charge-block", 5000, 5_000_000, fuzzCostLimitOneCent)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	start, _ := CountLines(r.cfg.CostFile)
	_ = r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000})
	if _, err := waitCostFlush(ctx, r.cfg.CostFile, start, 1); err != nil {
		return false, "first request cost not flushed: " + err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	blocked := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4})
	if !isCostLimitBlocked(blocked) {
		return false, fmt.Sprintf("expected block got %d", blocked.Status)
	}
	after, _ := CountLines(r.cfg.CostFile)
	if after > before {
		return false, fmt.Sprintf("blocked request added cost lines before=%d after=%d", before, after)
	}
	return true, "blocked request not cost-tracked"
}

func (r *Runner) costLimitIsolatedKeys(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	capped, err := kh.createWithCost(ctx, "fuzz-cost-capped", 5000, 5_000_000, fuzzCostLimitOneCent)
	if err != nil {
		return false, err.Error()
	}
	free, err := kh.createWithCost(ctx, "fuzz-cost-free", 5000, 5_000_000, 0)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	_ = r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: capped, ChaosRate: &zero, OutputTok: 50_000})
	if res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: capped, ChaosRate: &zero, OutputTok: 4}); !isCostLimitBlocked(res) {
		return false, "capped key should be blocked"
	}
	if res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: free, ChaosRate: &zero, OutputTok: 8}); res.Status != http.StatusOK {
		return false, fmt.Sprintf("uncapped key want 200 got %d", res.Status)
	}
	return true, "cost caps are per-key"
}

func (r *Runner) costLimitAdminByKeyStats(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-admin-stats", 5000, 5_000_000, fuzzCostLimitHigh)
	if err != nil {
		return false, err.Error()
	}
	cfgBefore, _ := r.admin.CostConfig(ctx)
	statsBefore := extractStats(cfgBefore)
	spendBefore := live.CostStatsSpendToday(statsBefore)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 20})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("status %d", res.Status)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cfg, err := r.admin.CostConfig(ctx)
		if err != nil {
			return false, err.Error()
		}
		stats := extractStats(cfg)
		if live.CostStatsSpendToday(stats) > spendBefore {
			if byKey, ok := stats["by_key"].([]any); ok && len(byKey) > 0 {
				return true, fmt.Sprintf("by_key entries=%d spend_today=%.6f", len(byKey), live.CostStatsSpendToday(stats))
			}
			return false, "spend increased but by_key missing"
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, "admin cost stats did not update"
}

func (r *Runner) costLimitUpdateRaisesCap(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-update", 5000, 5_000_000, fuzzCostLimitOneCent)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	_ = r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000})
	if res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4}); !isCostLimitBlocked(res) {
		return false, "expected block before limit raise"
	}
	raised := fuzzCostLimitHigh
	if _, err := r.admin.UpdateKey(ctx, key, live.UpdateKeyRequest{DailyCostLimit: &raised}); err != nil {
		return false, err.Error()
	}
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 8})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("after raise want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	return true, "raised daily_cost_limit allows more traffic"
}

func (r *Runner) costLimitUpdateRemovesCap(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-remove", 5000, 5_000_000, fuzzCostLimitOneCent)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	_ = r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000})
	if res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4}); !isCostLimitBlocked(res) {
		return false, "expected block before cap removal"
	}
	unlimited := int64(0)
	if _, err := r.admin.UpdateKey(ctx, key, live.UpdateKeyRequest{DailyCostLimit: &unlimited}); err != nil {
		return false, err.Error()
	}
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 8})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("after removal want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	return true, "daily_cost_limit=0 removes the cap"
}

func (r *Runner) costLimitConcurrentBoundary(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-cost-concurrent", 5000, 5_000_000, 10)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	results := r.proxy.Burst(ctx, 40, 20, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000, FakeOutcome: "success"})
	})
	ok, blocked := 0, 0
	for _, res := range results {
		switch {
		case res.Status == http.StatusOK:
			ok++
		case isCostLimitBlocked(res):
			blocked++
		}
	}
	if ok < 1 {
		return false, "expected at least one success before cap"
	}
	if blocked < 1 {
		return false, fmt.Sprintf("expected some blocks ok=%d blocked=%d", ok, blocked)
	}
	return true, fmt.Sprintf("ok=%d blocked=%d", ok, blocked)
}

// costLimitAtomicityStress hammers a single low-cap key with far more parallel
// requests than the cap can fund, to prove the Redis check-and-reserve is
// atomic under heavy contention. If the reservation were NOT atomic (e.g. a
// read-only fallback against the async recorder), a thundering herd would all
// observe spend-below-cap simultaneously, get admitted, and overshoot the cap
// by orders of magnitude. With an atomic Lua reservation, admission is
// serialized so the number of OK responses is bounded by roughly cap/cost.
//
// To make the admission bound (driven by the up-front *estimate*) directly
// comparable to recorded spend (driven by the reconciled *actual*), every
// request pins OutputTok == defaultEstimateOutputTokens (1024) so estimate ≈
// actual. The per-request cost is measured empirically on a high-cap key first.
func (r *Runner) costLimitAtomicityStress(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	zero := 0.0
	const estOutputTok = 1024 // == defaultEstimateOutputTokens in the proxy

	// Step 1: measure per-request cost E on a generous-cap key.
	warm, err := kh.createWithCost(ctx, "fuzz-atomic-warm", 100_000, 500_000_000, fuzzCostLimitHigh)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	w := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: warm, ChaosRate: &zero, OutputTok: estOutputTok})
	if w.Status != http.StatusOK {
		return false, fmt.Sprintf("warmup want 200 got %d body=%s", w.Status, truncate(w.Body, 120))
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1)
	if err != nil {
		return false, "warmup cost not recorded: " + err.Error()
	}
	perReq := recs[0].TotalCost
	if perReq <= 0 {
		return false, "warmup per-request cost was zero"
	}

	// Step 2: small cap, then slam it with many parallel requests. The cap is
	// sized (relative to the ~$0.0007 per-request estimate) so that ~cap/perReq
	// concurrent reservations fit before it fills — demonstrating the cap
	// filling progressively under contention, with recorded spend hugging just
	// under the cap, rather than either blocking everything or overshooting.
	const capCents int64 = 10 // $0.10
	capUSD := float64(capCents) / 100.0
	maxAllowed := int(math.Floor(capUSD/perReq)) + 1 // +1 for the boundary admission
	key, err := kh.createWithCost(ctx, "fuzz-atomic-stress", 100_000, 500_000_000, capCents)
	if err != nil {
		return false, err.Error()
	}

	const fired = 400
	const workers = 64
	burstBefore, _ := CountLines(r.cfg.CostFile)
	results := r.proxy.Burst(ctx, fired, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: estOutputTok})
	})
	ok, blocked, other := 0, 0, 0
	for _, res := range results {
		switch {
		case res.Status == http.StatusOK:
			ok++
		case isCostLimitBlocked(res):
			blocked++
		default:
			other++
		}
	}
	if ok < 1 {
		return false, "expected at least one success before cap"
	}
	if blocked < 1 {
		return false, fmt.Sprintf("expected blocks under cap ok=%d blocked=%d other=%d", ok, blocked, other)
	}

	// Atomicity invariant: admitted count cannot exceed the cap's worth plus a
	// small slack for rounding (estimate vs actual round-up). A non-atomic
	// reservation would admit close to `fired`.
	bound := maxAllowed + 12
	if ok > bound {
		return false, fmt.Sprintf(
			"OVERSHOOT: admitted ok=%d > bound=%d (maxAllowed=%d perReq=%.6f cap=%.4f fired=%d) — reservation not atomic",
			ok, bound, maxAllowed, perReq, capUSD, fired,
		)
	}

	// And recorded spend must also stay bounded once the async tracker flushes.
	burstRecs, err := waitCostFlush(ctx, r.cfg.CostFile, burstBefore, ok)
	if err != nil {
		return false, "burst cost not fully recorded: " + err.Error()
	}
	spent := SumCost(burstRecs)
	spendBound := capUSD + float64(bound)*perReq
	if spent > spendBound {
		return false, fmt.Sprintf("OVERSHOOT spend=%.6f > bound=%.6f (ok=%d cap=%.4f perReq=%.6f)",
			spent, spendBound, ok, capUSD, perReq)
	}
	return true, fmt.Sprintf(
		"atomic under load: fired=%d ok=%d blocked=%d other=%d (maxAllowed≈%d perReq=%.6f) spend=%.6f cap=%.4f",
		fired, ok, blocked, other, maxAllowed, perReq, spent, capUSD,
	)
}

// costRollupAggregation proves the Redis cost rollup (the by_key INCRBYFLOAT
// aggregation read by /admin/api/cost spend_today_usd) stays consistent with
// the authoritative per-record JSONL ledger under a concurrent write burst. A
// non-atomic float aggregation would drop updates and drift below the recorded
// sum. Scenarios run serially, so the global spend delta isolates this burst.
func (r *Runner) costRollupAggregation(ctx context.Context) (bool, string) {
	costBefore, err := r.admin.CostConfig(ctx)
	if err != nil {
		return false, "admin /cost: " + err.Error()
	}
	statsBefore, _ := costBefore["stats"].(map[string]any)
	if !live.CostStatsAvailable(statsBefore) {
		return false, "cost rollup stats unavailable (spend_today) — cannot verify aggregation"
	}
	spendBefore := live.CostStatsSpendToday(statsBefore)

	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.createWithCost(ctx, "fuzz-rollup-agg", 100_000, 500_000_000, fuzzCostLimitHigh)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	const fired = 200
	const workers = 64
	jsonlBefore, _ := CountLines(r.cfg.CostFile)
	results := r.proxy.Burst(ctx, fired, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 50_000})
	})
	ok := 0
	for _, res := range results {
		if res.Status == http.StatusOK {
			ok++
		}
	}
	if ok < 1 {
		return false, "no successful requests in rollup burst"
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, jsonlBefore, ok)
	if err != nil {
		return false, "jsonl ledger did not flush all records: " + err.Error()
	}
	jsonlSum := SumCost(recs)

	// The rollup may lag the ledger; poll until the global spend delta matches
	// the recorded sum within a rounding tolerance (1% + per-record rounding).
	tol := jsonlSum*0.01 + float64(ok)*0.0001
	deadline := time.Now().Add(10 * time.Second)
	var delta float64
	for time.Now().Before(deadline) {
		costAfter, err := r.admin.CostConfig(ctx)
		if err != nil {
			return false, err.Error()
		}
		statsAfter, _ := costAfter["stats"].(map[string]any)
		delta = live.CostStatsSpendToday(statsAfter) - spendBefore
		if math.Abs(delta-jsonlSum) <= tol {
			return true, fmt.Sprintf("rollup matches ledger: records=%d jsonlSum=%.6f rollupDelta=%.6f (tol=%.6f)", ok, jsonlSum, delta, tol)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, fmt.Sprintf("ROLLUP DRIFT: rollupDelta=%.6f vs jsonlSum=%.6f (records=%d tol=%.6f) — by_key aggregation lost updates", delta, jsonlSum, ok, tol)
}

// costFailedReleaseStress proves that failed (5xx) requests release their
// up-front cost reservation under concurrency. A burst of forced-500 requests
// (each reserves an estimate, then reconciles to zero on failure) must NOT
// permanently consume the per-KEY cap: a subsequent normal request still
// succeeds. A reservation leak on the failure path would exhaust the cap.
//
// The failure storm runs on a dedicated model so its circuit breaker (keyed by
// provider:model) opening does not interfere with verification: the follow-up
// check uses a DIFFERENT model on the same key (fresh circuit), isolating the
// per-key cost cap from per-model circuit state.
func (r *Runner) costFailedReleaseStress(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	// Small cap; the leaked estimates from 300 failures would dwarf it.
	const capCents int64 = 5 // $0.05
	key, err := kh.createWithCost(ctx, "fuzz-cost-failed-release", 100_000, 500_000_000, capCents)
	if err != nil {
		return false, err.Error()
	}
	const failModel = "cost-fail-storm"
	const checkModel = "cost-fail-check"
	zero := 0.0
	const fired = 300
	const workers = 64
	results := r.proxy.Burst(ctx, fired, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, Model: failModel, ChaosRate: &zero, FakeOutcome: "500", OutputTok: 1024})
	})
	failed := 0
	for _, res := range results {
		if res.Status >= 500 || isDegraded(res) {
			failed++
		}
	}
	if failed < 1 {
		return false, fmt.Sprintf("expected forced-500 failures, saw none (fired=%d)", fired)
	}
	// After the failure storm, reservations must be released. Poll a normal
	// request (on a fresh model with a closed circuit) until it succeeds; if
	// the cap was leaked it stays cost-blocked (402).
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: checkModel, ChaosRate: &zero, FakeOutcome: "success", OutputTok: 8})
		if res.Status == http.StatusOK {
			return true, fmt.Sprintf("reservations released after %d concurrent failures; per-key cap still admits", failed)
		}
		if !isCostLimitBlocked(res) {
			return false, fmt.Sprintf("post-failure request unexpected status %d body=%s", res.Status, truncate(res.Body, 120))
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false, fmt.Sprintf("RESERVATION LEAK: cap still blocked after %d concurrent failures released no budget", failed)
}

func (r *Runner) costLimitCreatePersistsLimit(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	want := int64(42)
	key, err := kh.createWithCost(ctx, "fuzz-cost-persist", 100, 50_000, want)
	if err != nil {
		return false, err.Error()
	}
	rec, err := r.admin.GetKey(ctx, key)
	if err != nil {
		return false, err.Error()
	}
	if rec.DailyCostLimit != want {
		return false, fmt.Sprintf("daily_cost_limit=%d want %d", rec.DailyCostLimit, want)
	}
	return true, fmt.Sprintf("daily_cost_limit=%d stored", rec.DailyCostLimit)
}
