package redact

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

type analyzeSemCtxKey struct{}

// withAnalyzeSem attaches a per-request concurrency semaphore to ctx if one is
// not already present. Leaf HTTP /analyze calls acquire from this semaphore so
// nested fan-out (runJSONScrubTasks x analyzeChunked) cannot multiply past
// AnalyzeConcurrency while independent concurrent requests stay isolated.
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
	done    chan struct{}
	cancel  context.CancelFunc
	waiters int
	spans   []Span
	err     error
}

// analyzeLeaf sends a single chunk/block to Presidio /analyze using the
// redactor's default entity scope and score threshold, coalescing concurrent
// identical texts across both sibling tasks and concurrent requests.
//
// Each waiter enforces its own timeout from ctx; the underlying HTTP call is
// canceled only when all waiters have abandoned it. On success the leader
// populates r.analyzeCache before releasing waiters so completed work survives
// even if a sibling chunk later times out.
func (r *Redactor) analyzeLeaf(ctx context.Context, text string) ([]Span, error) {
	if text == "" {
		return nil, nil
	}

	waitTimeout := AnalyzeTimeoutFromContext(ctx, r.cfg.Timeout)
	waitCtx, waitCancel := context.WithTimeout(ctx, waitTimeout)
	defer waitCancel()
	if err := waitCtx.Err(); err != nil {
		return nil, fmt.Errorf("redact: analyze call failed: %w", err)
	}

	key := sha256.Sum256([]byte(text))

	r.inflightMu.Lock()
	if r.inflight == nil {
		r.inflight = make(map[[32]byte]*inflightAnalyzeCall)
	}
	call, existing := r.inflight[key]
	if existing {
		call.waiters++
		r.inflightMu.Unlock()
	} else {
		callCtx, callCancel := context.WithCancel(context.Background())
		call = &inflightAnalyzeCall{
			done:    make(chan struct{}),
			cancel:  callCancel,
			waiters: 1,
		}
		r.inflight[key] = call
		sem := analyzeSemFromContext(ctx)
		r.inflightMu.Unlock()

		go r.runInflightAnalyze(key, call, callCtx, sem, text)
	}

	select {
	case <-call.done:
		if call.err != nil {
			return nil, call.err
		}
		return cloneSpans(call.spans), nil
	case <-waitCtx.Done():
		r.inflightMu.Lock()
		call.waiters--
		if call.waiters == 0 && r.inflight[key] == call {
			delete(r.inflight, key)
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
	sem chan struct{},
	text string,
) {
	defer call.cancel()

	spans, err := r.doAnalyzeHTTP(callCtx, sem, text, nil, r.cfg.ScoreThreshold)
	if err == nil && r.analyzeCache != nil {
		cacheCtx, cacheCancel := context.WithTimeout(context.Background(), time.Second)
		r.analyzeCache.Set(cacheCtx, text, spans)
		cacheCancel()
	}

	r.inflightMu.Lock()
	call.spans = spans
	call.err = err
	if r.inflight[key] == call {
		delete(r.inflight, key)
	}
	close(call.done)
	r.inflightMu.Unlock()
}
