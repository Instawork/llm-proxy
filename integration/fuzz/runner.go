package fuzz

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/integration/live"
)

type Runner struct {
	cfg    Config
	report *Report
	admin  *live.AdminClient
	proxy  *Proxy
}

func NewRunner(cfg Config) (*Runner, error) {
	report := &Report{Seed: cfg.Seed, StatusHistogram: StatusHistogram{}}
	admin, err := live.NewAdminClient(cfg.BaseURL, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	return &Runner{
		cfg:    cfg,
		report: report,
		admin:  admin,
		proxy:  NewProxy(cfg.BaseURL, cfg.Timeout, report),
	}, nil
}

func (r *Runner) Run(ctx context.Context) (*Report, error) {
	if r.cfg.ResetCostFile {
		_ = ResetCostFile(r.cfg.CostFile)
	}
	r.ensureCircuitReady(ctx)
	scenarios := ParseScenarioList(r.cfg.Scenario)
	for _, name := range scenarios {
		start := time.Now()
		pass, detail := r.runScenario(ctx, name)
		r.report.AddScenario(ScenarioResult{
			Name:    name,
			Pass:    pass,
			Detail:  detail,
			Elapsed: time.Since(start).Round(time.Millisecond).String(),
		})
		if !pass {
			break
		}
	}
	return r.report, nil
}

func (r *Runner) ensureCircuitReady(ctx context.Context) {
	kh := newKeyHelper(r.admin)
	key, err := kh.create(ctx, "fuzz-preflight", 10_000, 10_000_000)
	if err != nil {
		return
	}
	defer kh.cleanup(ctx)
	zero := 0.0
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, FakeOutcome: "success"})
		if res.Status == http.StatusOK {
			return
		}
		time.Sleep(time.Second)
	}
}

func (r *Runner) runScenario(ctx context.Context, name string) (bool, string) {
	switch name {
	case "ratelimit-key-rpm":
		return r.rateLimitKeyRPM(ctx)
	case "ratelimit-key-tpm":
		return r.rateLimitKeyTPM(ctx)
	case "ratelimit-race":
		return r.rateLimitRace(ctx)
	case "ratelimit-reconcile":
		return r.rateLimitReconcile(ctx)
	case "ratelimit-cancel-5xx":
		return r.rateLimitCancel5xx(ctx)
	case "cost-jsonl-count":
		return r.costJSONLCount(ctx)
	case "cost-token-math":
		return r.costTokenMath(ctx)
	case "cost-fuzzy-model":
		return r.costFuzzyModel(ctx)
	case "cost-unknown-model":
		return r.costUnknownModel(ctx)
	case "cost-no-charge-429":
		return r.costNoCharge429(ctx)
	case "cost-no-charge-degraded":
		return r.costNoChargeDegraded(ctx)
	case "cost-concurrent-async":
		return r.costConcurrentAsync(ctx)
	case "cost-admin-stats":
		return r.costAdminStats(ctx)
	case "circuit-random-trip":
		return r.circuitRandomTrip(ctx)
	case "circuit-recovery":
		return r.circuitRecovery(ctx)
	case "circuit-mixed":
		return r.circuitMixed(ctx)
	case "circuit-transient-retry":
		return r.circuitTransientRetry(ctx)
	case "latency-timeout":
		return r.latencyTimeout(ctx)
	default:
		return false, "unknown scenario"
	}
}

func (r *Runner) rateLimitKeyRPM(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	const limit = 5
	key, err := kh.create(ctx, "fuzz-rpm", limit, 50000)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	total := r.cfg.Workers * r.cfg.Requests
	if total < limit+3 {
		total = limit + 3
	}
	results := r.proxy.Burst(ctx, total, r.cfg.Workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero})
	})
	ok, denied := 0, 0
	for _, res := range results {
		if res.Status == http.StatusOK {
			ok++
		}
		if res.Status == http.StatusTooManyRequests {
			denied++
		}
	}
	if ok != limit {
		return false, fmt.Sprintf("want %d successes got %d (denied=%d total=%d)", limit, ok, denied, total)
	}
	if denied == 0 {
		return false, "expected some 429 responses"
	}
	return true, fmt.Sprintf("ok=%d denied=%d", ok, denied)
}

func (r *Runner) rateLimitKeyTPM(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-tpm", 1000, 50)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	big := strings.Repeat("token ", 4000)
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Content: big, ChaosRate: &zero})
	if res.Status != http.StatusTooManyRequests {
		return false, fmt.Sprintf("expected 429 got %d", res.Status)
	}
	return true, "token limit 429"
}

func (r *Runner) rateLimitRace(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-race", 3, 50000)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	results := r.proxy.Burst(ctx, 20, 10, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero})
	})
	var saw429 bool
	for _, res := range results {
		if res.Status == http.StatusTooManyRequests {
			saw429 = true
			if res.Headers.Get("Retry-After") == "" {
				return false, "429 missing Retry-After"
			}
		}
	}
	if !saw429 {
		return false, "expected at least one 429"
	}
	return true, "burst produced 429 with Retry-After"
}

func (r *Runner) rateLimitReconcile(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-reconcile", 100, 500)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	outTok := 100
	for i := 0; i < 3; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: outTok})
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("request %d status %d", i, res.Status)
		}
	}
	return true, "reconcile path completed without 429"
}

func (r *Runner) rateLimitCancel5xx(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cancel-5xx", 5, 50000)
	if err != nil {
		return false, err.Error()
	}
	failures := 0
	for i := 0; i < 5; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, FakeOutcome: "500"})
		if res.Status >= 500 {
			failures++
		}
	}
	if failures == 0 {
		return false, "expected 5xx from fake outcome 500"
	}
	time.Sleep(11 * time.Second)
	zero := 0.0
	ok := 0
	for i := 0; i < 5; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, FakeOutcome: "success"})
		if res.Status == http.StatusOK {
			ok++
		}
	}
	if ok != 5 {
		return false, fmt.Sprintf("after 5xx cancel expected 5 ok got %d", ok)
	}
	return true, fmt.Sprintf("failures=%d then rpm budget intact", failures)
}

func (r *Runner) costJSONLCount(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cost-count", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	n := r.cfg.Requests
	if n < 5 {
		n = 5
	}
	for i := 0; i < n; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 10})
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("chat %d status %d", i, res.Status)
		}
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, n)
	if err != nil {
		return false, err.Error()
	}
	r.report.CostLinesDelta = len(recs)
	r.report.CostLinesExpected = n
	if len(recs) != n {
		return false, fmt.Sprintf("want %d cost lines got %d", n, len(recs))
	}
	return true, fmt.Sprintf("%d jsonl records", len(recs))
}

func (r *Runner) costTokenMath(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cost-math", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	outTok := 32
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: outTok, Content: "short"})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("status %d", res.Status)
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1)
	if err != nil {
		return false, err.Error()
	}
	rec := recs[0]
	expected := ExpectedOpenAICost(rec.InputTokens, outTok)
	r.report.CostTotalObserved = rec.TotalCost
	r.report.CostTotalExpected = expected
	if rec.OutputTokens != outTok {
		return false, fmt.Sprintf("output tokens %d want %d", rec.OutputTokens, outTok)
	}
	if rec.TotalCost != expected {
		return false, fmt.Sprintf("total_cost %.6f want %.6f", rec.TotalCost, expected)
	}
	return true, fmt.Sprintf("cost=%.6f in=%d out=%d", rec.TotalCost, rec.InputTokens, rec.OutputTokens)
}

func (r *Runner) costFuzzyModel(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-fuzzy-model", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey: key, Model: "gpt4o-mini", ChaosRate: &zero, OutputTok: 8,
	})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("status %d", res.Status)
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1)
	if err != nil {
		return false, err.Error()
	}
	rec := recs[0]
	if !rec.IsEstimate {
		return false, "expected is_estimate true"
	}
	if rec.MatchedModel == "" {
		return false, "expected matched_model set"
	}
	return true, fmt.Sprintf("matched_model=%s cost=%.6f", rec.MatchedModel, rec.TotalCost)
}

func (r *Runner) costUnknownModel(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-unknown-model", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey: key, Model: "not-a-real-model-xyz", ChaosRate: &zero, OutputTok: 4,
	})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("status %d", res.Status)
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1)
	if err != nil {
		return false, err.Error()
	}
	if recs[0].TotalCost != 0 {
		return false, fmt.Sprintf("expected zero cost got %.6f", recs[0].TotalCost)
	}
	return true, "unknown model recorded with zero cost"
}

func (r *Runner) costNoCharge429(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-no-cost-429", 1, 50000)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	first := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero})
	if first.Status != http.StatusOK {
		return false, fmt.Sprintf("first request want 200 got %d", first.Status)
	}
	before, _ := CountLines(r.cfg.CostFile)
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero})
	if res.Status != http.StatusTooManyRequests {
		return false, fmt.Sprintf("expected 429 got %d", res.Status)
	}
	after, _ := CountLines(r.cfg.CostFile)
	if after > before {
		return false, fmt.Sprintf("429 should not add cost lines (before=%d after=%d)", before, after)
	}
	return true, "no cost lines on 429"
}

func (r *Runner) costNoChargeDegraded(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-no-cost-degraded", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, TestMode: "force_degraded"})
	if res.Status != http.StatusServiceUnavailable {
		return false, fmt.Sprintf("expected 503 got %d", res.Status)
	}
	after, _ := CountLines(r.cfg.CostFile)
	if after > before {
		return false, "degraded should not add cost lines"
	}
	return true, "no cost on force_degraded"
}

func (r *Runner) costConcurrentAsync(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cost-async", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	n := r.cfg.Workers * r.cfg.Requests
	if n < 30 {
		n = 30
	}
	workers := r.cfg.Workers
	if workers < 10 {
		workers = 10
	}
	results := r.proxy.Burst(ctx, n, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4})
	})
	ok := 0
	for _, res := range results {
		if res.Status == http.StatusOK {
			ok++
		}
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, ok)
	if err != nil {
		return false, err.Error()
	}
	if len(recs) != ok {
		return false, fmt.Sprintf("want %d cost records got %d", ok, len(recs))
	}
	return true, fmt.Sprintf("ok=%d records=%d", ok, len(recs))
}

func (r *Runner) costAdminStats(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-admin-stats", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	cfgBefore, _ := r.admin.CostConfig(ctx)
	spendBefore := live.CostStatsSpendToday(extractStats(cfgBefore))
	before, _ := CountLines(r.cfg.CostFile)
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 20})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("status %d", res.Status)
	}
	_, _ = waitCostFlush(ctx, r.cfg.CostFile, before, 1)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		cfg, err := r.admin.CostConfig(ctx)
		if err != nil {
			return false, err.Error()
		}
		stats := extractStats(cfg)
		if live.CostStatsAvailable(stats) && live.CostStatsSpendToday(stats) > spendBefore {
			return true, fmt.Sprintf("spend_today_usd=%.6f", live.CostStatsSpendToday(stats))
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false, "spend_today_usd did not increase"
}

func (r *Runner) circuitRandomTrip(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-circuit-trip", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	rate := r.cfg.ChaosRate
	if rate <= 0 {
		rate = 1.0
	}
	for i := 0; i < 40; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &rate})
		if strings.Contains(res.Body, degradedSignal) {
			return true, fmt.Sprintf("degraded after %d attempts", i+1)
		}
	}
	return false, "circuit did not fast-fail with degraded signal"
}

func (r *Runner) circuitRecovery(ctx context.Context) (bool, string) {
	ok, detail := r.circuitRandomTrip(ctx)
	if !ok {
		return false, "prerequisite trip failed: " + detail
	}
	time.Sleep(11 * time.Second)
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-circuit-recover", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("post-cooldown want 200 got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	return true, "circuit recovered after cooldown"
}

func (r *Runner) circuitMixed(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-circuit-mixed", 500, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	rate := 0.4
	if r.cfg.ChaosRate > 0 {
		rate = r.cfg.ChaosRate
	}
	n := r.cfg.Workers * r.cfg.Requests
	if n < 25 {
		n = 25
	}
	results := r.proxy.Burst(ctx, n, r.cfg.Workers, func(c context.Context) ChatResult {
		opts := ChatOpts{APIKey: key, ChaosRate: &rate, OutputTok: 6}
		return r.proxy.OpenAIChat(c, opts)
	})
	ok := 0
	for _, res := range results {
		if res.Status == http.StatusOK {
			ok++
		}
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, ok)
	if err != nil && ok > 0 {
		return false, err.Error()
	}
	if len(recs) != ok {
		return false, fmt.Sprintf("cost lines %d != successes %d", len(recs), ok)
	}
	return true, fmt.Sprintf("mixed ok=%d total=%d", ok, n)
}

func (r *Runner) circuitTransientRetry(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-transient-retry", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, TestMode: "force_transient_recover"})
	if res.Status != http.StatusOK {
		return false, fmt.Sprintf("want 200 after retry got %d body=%s", res.Status, truncate(res.Body, 120))
	}
	return true, "force_transient_recover succeeded"
}

func (r *Runner) latencyTimeout(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-latency", 1000, 500000)
	if err != nil {
		return false, err.Error()
	}
	before, _ := CountLines(r.cfg.CostFile)
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	zero := 0.0
	res := r.proxy.OpenAIChat(shortCtx, ChatOpts{APIKey: key, ChaosRate: &zero, LatencyMS: 500})
	if res.Err == nil {
		return false, "expected timeout error"
	}
	after, _ := CountLines(r.cfg.CostFile)
	if after > before {
		return false, "timeout should not write cost"
	}
	return true, "client timeout, no cost line"
}

func extractStats(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	stats, _ := cfg["stats"].(map[string]any)
	return stats
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
