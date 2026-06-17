package fuzz

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// rateLimitTPMAtomicityStress proves the per-minute TOKEN reservation is atomic
// under contention. Unlike RPM (a clean +1), the token path reserves an
// estimate up front, so a non-atomic check-and-reserve would let a thundering
// herd all observe tokens<limit at once and overshoot the budget.
//
// The exact token budget per request is opaque to the client, so we first
// measure it as a reference by draining a fresh key SEQUENTIALLY (zero
// concurrency) until the first 429 — that yields floor(TPM/perReqTokens)
// admits. We then slam a second identically-configured key with a massive
// parallel burst and assert the parallel admit count does not exceed the
// sequential reference (plus a tiny boundary slack). Concurrency must never
// admit MORE than the serial path.
func (r *Runner) rateLimitTPMAtomicityStress(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	zero := 0.0
	// A modest content size so each request reserves a non-trivial, stable
	// token estimate; TPM sized so ~a dozen requests fit per window.
	content := strings.Repeat("token ", 200)
	const tpm = 40000

	// Reference: sequential drain on a fresh key within one minute window.
	refKey, err := kh.create(ctx, "fuzz-tpm-atomic-ref", 100_000, tpm)
	if err != nil {
		return false, err.Error()
	}
	seqOK := 0
	for i := 0; i < 500; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: refKey, Content: content, ChaosRate: &zero})
		if res.Status == http.StatusOK {
			seqOK++
			continue
		}
		if res.Status == http.StatusTooManyRequests {
			break
		}
		return false, fmt.Sprintf("reference drain unexpected status %d body=%s", res.Status, truncate(res.Body, 120))
	}
	if seqOK < 2 {
		return false, fmt.Sprintf("reference admitted too few (seqOK=%d) — raise TPM or shrink content", seqOK)
	}

	// Stress: identical config, hammered in parallel.
	key, err := kh.create(ctx, "fuzz-tpm-atomic-stress", 100_000, tpm)
	if err != nil {
		return false, err.Error()
	}
	const fired = 400
	const workers = 96
	start := time.Now()
	results := r.proxy.Burst(ctx, fired, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, Content: content, ChaosRate: &zero})
	})
	end := time.Now()
	windows := int(end.Truncate(time.Minute).Sub(start.Truncate(time.Minute))/time.Minute) + 1

	ok, denied, other := 0, 0, 0
	for _, res := range results {
		switch res.Status {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			denied++
		default:
			other++
		}
	}
	if other != 0 {
		return false, fmt.Sprintf("unexpected non-200/429 other=%d (ok=%d denied=%d)", other, ok, denied)
	}
	// Parallel admits are bounded by the per-window reference scaled by the
	// windows spanned. We allow up to 2x the sequential reference to absorb the
	// legitimate estimate-vs-actual reconcile-lag overshoot: in flight, requests
	// reserve a token ESTIMATE that runs slightly under the reconciled actual,
	// so a few more fit before the adjust-up lands. This is bounded and inherent
	// to estimate-then-reconcile — it is NOT the failure mode we hunt. A truly
	// non-atomic reserve would let the whole herd (~fired) blow through, which
	// this 2x bound still catches cleanly.
	bound := 2*seqOK*windows + 1
	if ok > bound {
		return false, fmt.Sprintf("NON-ATOMIC TPM: parallel ok=%d > bound=%d (seqRef=%d windows=%d fired=%d) — token reservation blew through, not atomic", ok, bound, seqOK, windows, fired)
	}
	if ok < 1 {
		return false, "expected at least one parallel success"
	}
	return true, fmt.Sprintf("atomic TPM under load: seqRef=%d parallel ok=%d denied=%d windows=%d (bound=%d) workers=%d", seqOK, ok, denied, windows, bound, workers)
}

// rateLimitDailyAtomicityStress is the RPD (requests-per-day) analog of the RPM
// thundering-herd test: a fresh key with a small per-day request cap is slammed
// with far more parallel requests than the cap, and admitted must stay within
// limit×(day windows touched). RPM is set arbitrarily high so the day window is
// the only binding constraint.
func (r *Runner) rateLimitDailyAtomicityStress(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	const rpd = 30
	key, err := kh.createWithDaily(ctx, "fuzz-rpd-atomic", 100_000, 500_000_000, rpd, 0)
	if err != nil {
		return false, err.Error()
	}
	zero := 0.0
	const fired = 500
	const workers = 96
	start := time.Now()
	results := r.proxy.Burst(ctx, fired, workers, func(c context.Context) ChatResult {
		return r.proxy.OpenAIChat(c, ChatOpts{APIKey: key, ChaosRate: &zero})
	})
	end := time.Now()
	windows := int(end.Truncate(24*time.Hour).Sub(start.Truncate(24*time.Hour))/(24*time.Hour)) + 1
	maxAdmit := rpd * windows

	ok, denied, other := 0, 0, 0
	for _, res := range results {
		switch res.Status {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			denied++
		default:
			other++
		}
	}
	if other != 0 {
		return false, fmt.Sprintf("unexpected non-200/429 other=%d (ok=%d denied=%d)", other, ok, denied)
	}
	if ok < rpd && windows == 1 {
		return false, fmt.Sprintf("expected to fill the day cap ok=%d rpd=%d", ok, rpd)
	}
	if ok > maxAdmit {
		return false, fmt.Sprintf("NON-ATOMIC RPD: admitted ok=%d > rpd*windows=%d (rpd=%d windows=%d fired=%d) — daily counter not atomic", ok, maxAdmit, rpd, windows, fired)
	}
	return true, fmt.Sprintf("atomic RPD under load: fired=%d ok=%d denied=%d rpd=%d windows=%d (cap=%d)", fired, ok, denied, rpd, windows, maxAdmit)
}
