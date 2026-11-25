package redact

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

type analyzeSemCtxKey struct{}

// withAnalyzeSem attaches a per-request concurrency semaphore to ctx if one is
// not already present. Leaf HTTP /analyze calls acquire from this semaphore
// before registering in the in-flight map so nested fan-out
// (runJSONScrubTasks x analyzeChunked) cannot multiply past AnalyzeConcurrency
// while independent concurrent requests stay isolated and never queue behind
// another request's semaphore.
func withAnalyzeSem(ctx context.Context, limit int) context.Context {
	if limit <= 0 {
		limit = defaultAnalyzeConcurrency
	}
	if analyzeSemFromContext(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, analyzeSemCtxKey{}, make(chan struct{}, limit))
}

func analyzeSemFromContext(ctx context.Context) chan struct{} {
	if v, ok := ctx.Value(analyzeSemCtxKey{}).(chan struct{}); ok {
		return v
	}
	return nil
}

type inflightAnalyzeCall struct {
	done      chan struct{}
	cancel    context.CancelFunc
	startedAt time.Time
	deadline  time.Time
	waiters   int
	spans     []Span
	err       error
}

// canJoin reports whether a new waiter with waitBudget should coalesce onto an
// existing in-flight call. Rejecting calls that have already expired or run for
// at least half the waiter's budget prevents one hung sidecar call from
// poisoning every overlapping arrival on a shared text (such as a system prompt).
func (c *inflightAnalyzeCall) canJoin(now time.Time, waitBudget time.Duration) bool {
	if !now.Before(c.deadline) {
		return false
	}
	if waitBudget <= 0 {
		return false
	}
	return now.Sub(c.startedAt) < waitBudget/2
}

// analyzeLeaf sends a single chunk/block to Presidio /analyze using the
// redactor's default entity scope and score threshold, coalescing concurrent
// identical texts across both sibling tasks and concurrent requests.
//
// Each waiter enforces its own timeout from ctx; the underlying HTTP call has
// a bounded deadline and is canceled immediately if all waiters abandon it.
// Local memory cache layers are written before waiters are released while
// network-backed cache writes (Redis) run off the critical path.
func (r *Redactor) analyzeLeaf(ctx context.Context, text string) ([]Span, error) {
	if text == "" {
		return nil, nil
	}

	waitBudget := AnalyzeTimeoutFromContext(ctx, r.cfg.Timeout)
	if dl, ok := ctx.Deadline(); ok {
		if rem := time.Until(dl); rem < waitBudget {
			waitBudget = rem
		}
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, waitBudget)
	defer waitCancel()
	if err := waitCtx.Err(); err != nil {
		return nil, fmt.Errorf("redact: analyze call failed: %w", err)
	}

	key := sha256.Sum256([]byte(text))
	now := time.Now()

	// Fast path: join an already-active, non-stale in-flight call without
	// consuming a semaphore slot.
	r.inflightMu.Lock()
	if existing, ok := r.inflight[key]; ok && existing.canJoin(now, waitBudget) {
		existing.waiters++
		r.inflightMu.Unlock()
		return r.awaitInflight(waitCtx, key, existing)
	}
	r.inflightMu.Unlock()

	// Acquire our own request's concurrency slot BEFORE registering in the
	// in-flight map so another independent request never coalesces onto a call
	// that is still queued behind our semaphore.
	sem := analyzeSemFromContext(ctx)
	var releaseSem func()
	if sem != nil {
		select {
		case sem <- struct{}{}:
			releaseSem = func() { <-sem }
		case <-waitCtx.Done():
			return nil, fmt.Errorf("redact: analyze call failed: %w", waitCtx.Err())
		}
	}

	now = time.Now()
	r.inflightMu.Lock()
	if r.inflight == nil {
		r.inflight = make(map[[32]byte]*inflightAnalyzeCall)
	}
	if existing, ok := r.inflight[key]; ok && existing.canJoin(now, waitBudget) {
		existing.waiters++
		r.inflightMu.Unlock()
		if releaseSem != nil {
			releaseSem()
		}
		return r.awaitInflight(waitCtx, key, existing)
	}

	callTimeout := max(waitBudget, r.cfg.Timeout)
	callCtx, callCancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	call := &inflightAnalyzeCall{
		done:      make(chan struct{}),
		cancel:    callCancel,
		startedAt: now,
		deadline:  now.Add(callTimeout),
		waiters:   1,
	}
	r.inflight[key] = call
	r.inflightMu.Unlock()

	go r.runInflightAnalyze(key, call, callCtx, releaseSem, text)

	return r.awaitInflight(waitCtx, key, call)
}

func (r *Redactor) awaitInflight(
	waitCtx context.Context,
	key [32]byte,
	call *inflightAnalyzeCall,
) ([]Span, error) {
	select {
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		return cloneSpans(call.spans), nil
	case <-waitCtx.Done():
		r.inflightMu.Lock()
		call.waiters--
		if call.waiters == 0 {
			if r.inflight[key] == call {
				delete(r.inflight, key)
			}
			call.cancel()
		}
		r.inflightMu.Unlock()
		return nil, fmt.Errorf("redact: analyze call failed: %w", waitCtx.Err())
	}
}

func (r *Redactor) runInflightAnalyze(
	key [32]byte,
	call *inflightAnalyzeCall,
	callCtx context.Context,
	releaseSem func(),
	text string,
) {
	defer call.cancel()

	spans, err := r.doAnalyzeHTTP(callCtx, nil, text, nil, r.cfg.ScoreThreshold)
	if releaseSem != nil {
		releaseSem()
	}

	// Populate local (in-process memory) cache synchronously before waking
	// waiters so immediate retries within the process observe the hit with
	// zero I/O overhead. Network-backed cache writes (Redis) happen after
	// close(call.done) so a slow or stalled Redis never stalls requests.
	if err == nil {
		r.cacheSetLocal(text, spans)
	}

	r.inflightMu.Lock()
	call.spans = spans
	call.err = err
	if r.inflight[key] == call {
		delete(r.inflight, key)
	}
	close(call.done)
	r.inflightMu.Unlock()

	if err == nil {
		r.cacheSetRemoteBounded(text, spans)
	}
}

// cacheGetLocal reads only from fast in-process cache layers (no network I/O).
func (r *Redactor) cacheGetLocal(text string) ([]Span, bool) {
	if r.analyzeCache == nil {
		return nil, false
	}
	if split, ok := r.analyzeCache.(splitAnalyzeCache); ok {
		return split.getLocal(text)
	}
	return nil, false
}

// cacheSetLocal writes only to fast in-process cache layers (no network I/O).
func (r *Redactor) cacheSetLocal(text string, spans []Span) {
	if r.analyzeCache == nil {
		return
	}
	if split, ok := r.analyzeCache.(splitAnalyzeCache); ok {
		split.setLocal(text, spans)
	}
}

// cacheSetRemoteBounded writes to network-backed or custom cache layers under a
// tight timeout and bounded concurrency semaphore, dropping best-effort writes
// under backpressure rather than accumulating goroutines during a Redis stall.
func (r *Redactor) cacheSetRemoteBounded(text string, spans []Span) {
	if r.analyzeCache == nil {
		return
	}
	if _, isMem := r.analyzeCache.(*memoryAnalyzeCache); isMem {
		return
	}
	if r.cacheWriteSem != nil {
		select {
		case r.cacheWriteSem <- struct{}{}:
			defer func() { <-r.cacheWriteSem }()
		default:
			return
		}
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), analyzeCacheWriteTimeout)
	defer cancel()
	if split, ok := r.analyzeCache.(splitAnalyzeCache); ok {
		split.setRemote(writeCtx, text, spans)
		return
	}
	r.analyzeCache.Set(writeCtx, text, spans)
}

// cacheSet writes local layers synchronously and dispatches any remote/custom
// layer write asynchronously under r.cacheWriteSem so callers never block on
// degraded Redis.
func (r *Redactor) cacheSet(text string, spans []Span) {
	if r.analyzeCache == nil {
		return
	}
	r.cacheSetLocal(text, spans)
	if _, isMem := r.analyzeCache.(*memoryAnalyzeCache); isMem {
		return
	}
	if r.cacheWriteSem != nil {
		select {
		case r.cacheWriteSem <- struct{}{}:
		default:
			return
		}
		go func(spansCopy []Span) {
			defer func() { <-r.cacheWriteSem }()
			writeCtx, cancel := context.WithTimeout(context.Background(), analyzeCacheWriteTimeout)
			defer cancel()
			if split, ok := r.analyzeCache.(splitAnalyzeCache); ok {
				split.setRemote(writeCtx, text, spansCopy)
				return
			}
			r.analyzeCache.Set(writeCtx, text, spansCopy)
		}(cloneSpans(spans))
		return
	}
	go func(spansCopy []Span) {
		writeCtx, cancel := context.WithTimeout(context.Background(), analyzeCacheWriteTimeout)
		defer cancel()
		if split, ok := r.analyzeCache.(splitAnalyzeCache); ok {
			split.setRemote(writeCtx, text, spansCopy)
			return
		}
		r.analyzeCache.Set(writeCtx, text, spansCopy)
	}(cloneSpans(spans))
}
