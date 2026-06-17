package fuzz

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
)

// costCacheTokenNoInflation guards against the single most common LLM
// cost-tracking bug seen in the wild: double-counting prompt-cache tokens.
//
// OpenAI (and Gemini) follow the INCLUSIVE convention — usage.prompt_tokens
// already contains the cached portion, and prompt_tokens_details.cached_tokens
// merely describes how many of those were a cache hit. A buggy proxy that adds
// cached_tokens onto the input count inflates billed input by up to ~2x for
// cache-heavy traffic.
//
// We fire two byte-identical requests that differ ONLY in the synthetic
// cached-token count and assert the recorded input tokens and cost are
// identical. No knowledge of the exact estimate is required — the request
// bodies are identical, so any divergence is a double-count regression.
func (r *Runner) costCacheTokenNoInflation(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cache-token", 1000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	const content = "the quick brown fox jumps over the lazy dog repeatedly"
	const outTok = 12

	before, _ := CountLines(r.cfg.CostFile)

	// Request A: no cache hit.
	resA := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey: key, ChaosRate: &zero, Content: content, OutputTok: outTok,
	})
	if resA.Status != http.StatusOK {
		return false, fmt.Sprintf("no-cache request want 200 got %d", resA.Status)
	}
	if _, err := waitCostFlush(ctx, r.cfg.CostFile, before, 1); err != nil {
		return false, err.Error()
	}

	// Request B: identical body, but the entire prompt is a cache hit.
	resB := r.proxy.OpenAIChat(ctx, ChatOpts{
		APIKey: key, ChaosRate: &zero, Content: content, OutputTok: outTok,
		CachedTokens: 1_000_000, // clamped to prompt_tokens by the fake upstream
	})
	if resB.Status != http.StatusOK {
		return false, fmt.Sprintf("cached request want 200 got %d", resB.Status)
	}
	recs, err := waitCostFlush(ctx, r.cfg.CostFile, before, 2)
	if err != nil {
		return false, err.Error()
	}
	if len(recs) < 2 {
		return false, fmt.Sprintf("want 2 cost records got %d", len(recs))
	}
	a, b := recs[0], recs[1]
	if a.InputTokens <= 0 || b.InputTokens <= 0 {
		return false, fmt.Sprintf("non-positive input tokens a=%d b=%d", a.InputTokens, b.InputTokens)
	}
	if a.InputTokens != b.InputTokens {
		return false, fmt.Sprintf("cached request inflated input tokens: no-cache=%d cached=%d (cache double-counted)", a.InputTokens, b.InputTokens)
	}
	if a.TotalCost != b.TotalCost {
		return false, fmt.Sprintf("cached request inflated cost: no-cache=%.6f cached=%.6f", a.TotalCost, b.TotalCost)
	}
	return true, fmt.Sprintf("cache tokens not double-counted (input=%d cost=%.6f for both)", a.InputTokens, a.TotalCost)
}

// rateLimitKeyRPD exercises the per-day request cap (RPD), a window the fuzzer
// did not previously cover (only per-minute RPM/TPM). Beyond the count, it
// asserts the 429 carries the headers clients need to back off correctly —
// Retry-After plus X-RateLimit-* — and that the block is attributed to the
// DAY window / requests metric, proving it was the daily cap (not RPM) that
// fired. Missing/garbage Retry-After is a classic thundering-herd trigger.
func (r *Runner) rateLimitKeyRPD(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	const rpd = 5
	// High per-minute caps so only the daily cap can trip within the run.
	key, err := kh.createWithDaily(ctx, "fuzz-rpd", 100000, 100_000_000, rpd, 0)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	total := rpd + 3
	ok, denied := 0, 0
	var firstDenied ChatResult
	for i := 0; i < total; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, ChaosRate: &zero, OutputTok: 4})
		switch res.Status {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			if denied == 0 {
				firstDenied = res
			}
			denied++
		default:
			return false, fmt.Sprintf("request %d unexpected status %d body=%s", i, res.Status, truncate(res.Body, 120))
		}
	}
	if ok != rpd {
		return false, fmt.Sprintf("want exactly %d allowed got %d (denied=%d)", rpd, ok, denied)
	}
	if denied == 0 {
		return false, "expected at least one 429 after exceeding daily cap"
	}
	// Header hygiene on the first denial.
	ra := firstDenied.Headers.Get("Retry-After")
	if secs, err := strconv.Atoi(ra); err != nil || secs <= 0 {
		return false, fmt.Sprintf("429 missing/invalid Retry-After=%q", ra)
	}
	if window := firstDenied.Headers.Get("X-RateLimit-Window"); window != "day" {
		return false, fmt.Sprintf("expected X-RateLimit-Window=day got %q (daily cap not the limiter that fired)", window)
	}
	if metric := firstDenied.Headers.Get("X-RateLimit-Metric"); metric != "requests" {
		return false, fmt.Sprintf("expected X-RateLimit-Metric=requests got %q", metric)
	}
	if lim := firstDenied.Headers.Get("X-RateLimit-Limit"); lim != strconv.Itoa(rpd) {
		return false, fmt.Sprintf("expected X-RateLimit-Limit=%d got %q", rpd, lim)
	}
	return true, fmt.Sprintf("rpd cap: ok=%d denied=%d retry_after=%ss window=day", ok, denied, ra)
}
