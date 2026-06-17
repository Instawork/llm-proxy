package fuzz

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Circuit-breaker end-to-end scenarios.
//
// These exercise the assembled proxy (enforce mode + redis backend + per-model
// keying via the model extractor) rather than the circuit package in isolation.
// The breaker is keyed by provider:model, so each scenario uses a UNIQUE model
// name to get an isolated breaker that cannot contaminate other scenarios (or
// the default gpt-4o-mini key the chaos scenarios trip).
//
// Config assumptions (configs/fuzz.yml): mode=enforce, failure_threshold=3,
// max_transient_retries=2, retry_contribution_mode=log (so only terminal
// failures count toward the threshold), cooldown_seconds=10.

// circuitTripAttempts is how many terminal-failure requests we send to push a
// per-model breaker past failure_threshold (3) with margin.
const circuitTripAttempts = 4

// circuitCooldownWait must exceed cooldown_seconds (10) so the breaker can move
// Open -> Half-Open for a probe.
const circuitCooldownWait = 13 * time.Second

// isDegraded reports whether a result is a proxy-synthesised circuit
// degradation (503 + the degraded body marker), as opposed to a real upstream
// status.
func isDegraded(res ChatResult) bool {
	return res.Status == http.StatusServiceUnavailable && strings.Contains(res.Body, degradedSignal)
}

// tripModel sends forced-500 requests to open the per-model breaker. Returns
// true once a request comes back as a degraded synthetic response (which proves
// terminal failures are being recorded).
func (r *Runner) tripModel(ctx context.Context, key, model string) bool {
	degraded := false
	for i := 0; i < circuitTripAttempts; i++ {
		res := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "500"})
		if isDegraded(res) {
			degraded = true
		}
	}
	return degraded
}

// circuitPerModelIsolation proves that opening one model's breaker fast-fails
// that model (even when the upstream would have succeeded) while a DIFFERENT
// model on the same provider/key keeps serving traffic.
func (r *Runner) circuitPerModelIsolation(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cb-isolation", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	const badModel = "cb-iso-bad"
	const goodModel = "cb-iso-good"

	if !r.tripModel(ctx, key, badModel) {
		return false, "bad model never returned degraded while tripping"
	}

	// Once OPEN, even a request the upstream would happily serve must be
	// fast-failed without touching the fake upstream.
	probe := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: badModel, FakeOutcome: "success"})
	if !isDegraded(probe) {
		return false, fmt.Sprintf("open model want degraded 503 got %d body=%s", probe.Status, truncate(probe.Body, 120))
	}

	// A sibling model on the same key/provider must be unaffected.
	healthy := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: goodModel, FakeOutcome: "success"})
	if healthy.Status != http.StatusOK {
		return false, fmt.Sprintf("healthy sibling model want 200 got %d body=%s", healthy.Status, truncate(healthy.Body, 120))
	}
	return true, "open breaker isolated to one model; sibling model still 200"
}

// circuitHalfOpenRecovers proves the Open -> Half-Open -> Closed path: after the
// cooldown a single successful probe closes the breaker and traffic resumes.
func (r *Runner) circuitHalfOpenRecovers(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cb-halfopen-recover", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	const model = "cb-half-recover"

	if !r.tripModel(ctx, key, model) {
		return false, "model never returned degraded while tripping"
	}
	// Confirm it is genuinely OPEN (fast-fail on a would-succeed request).
	if open := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "success"}); !isDegraded(open) {
		return false, fmt.Sprintf("pre-cooldown want degraded got %d", open.Status)
	}

	time.Sleep(circuitCooldownWait)

	// Half-open probe that succeeds should close the breaker.
	probe := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "success"})
	if probe.Status != http.StatusOK {
		return false, fmt.Sprintf("half-open probe want 200 got %d body=%s", probe.Status, truncate(probe.Body, 120))
	}
	// Closed: subsequent traffic flows normally.
	after := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "success"})
	if after.Status != http.StatusOK {
		return false, fmt.Sprintf("post-recovery want 200 got %d", after.Status)
	}
	return true, "open -> half-open probe success -> closed"
}

// circuitHalfOpenReopens proves a FAILED half-open probe re-opens the breaker
// for another cooldown instead of closing it.
func (r *Runner) circuitHalfOpenReopens(ctx context.Context) (bool, string) {
	kh := newKeyHelper(r.admin)
	defer kh.cleanup(ctx)
	key, err := kh.create(ctx, "fuzz-cb-halfopen-reopen", 5000, 5_000_000)
	if err != nil {
		return false, err.Error()
	}
	const model = "cb-half-reopen"

	if !r.tripModel(ctx, key, model) {
		return false, "model never returned degraded while tripping"
	}

	time.Sleep(circuitCooldownWait)

	// Half-open probe that FAILS must re-open the breaker.
	probe := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "500"})
	if !isDegraded(probe) {
		return false, fmt.Sprintf("failed half-open probe want degraded got %d body=%s", probe.Status, truncate(probe.Body, 120))
	}
	// Immediately after a failed probe the breaker is OPEN again (fresh
	// cooldown), so a would-succeed request is fast-failed without upstream.
	reblocked := r.proxy.OpenAIChat(ctx, ChatOpts{APIKey: key, Model: model, FakeOutcome: "success"})
	if !isDegraded(reblocked) {
		return false, fmt.Sprintf("after failed probe want re-opened degraded got %d body=%s", reblocked.Status, truncate(reblocked.Body, 120))
	}
	return true, "open -> half-open probe failure -> re-opened"
}
