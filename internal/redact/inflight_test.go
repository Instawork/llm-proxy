package redact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAnalyzeChunked_PerChunkCacheSurvivesPartialTimeoutAndAcceleratesRetry
// reproduces the Nov 24 2026 ~400 KB fail-closed retry loop: when a large
// multi-chunk field times out in a later wave, chunks that already completed
// must stay cached so a retry scans only the remaining chunks instead of
// repeating the exact same timeout indefinitely.
func TestAnalyzeChunked_PerChunkCacheSurvivesPartialTimeoutAndAcceleratesRetry(t *testing.T) {
	const (
		ssnA = "111-22-3333"
		ssnB = "444-55-6666"
	)

	var calls atomic.Int32
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		time.Sleep(45 * time.Millisecond)

		var spans []Span
		for _, ssn := range []string{ssnA, ssnB} {
			if idx := strings.Index(payload.Text, ssn); idx >= 0 {
				spans = append(spans, Span{
					Start:      idx,
					End:        idx + len(ssn),
					EntityType: "US_SSN",
					Score:      0.95,
				})
			}
		}
		_ = json.NewEncoder(w).Encode(spans)
	})

	fp := AnalyzeCacheFingerprint(Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	})
	cache, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
	}, fp)
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            time.Second,
		ChunkChars:         1200,
		AnalyzeConcurrency: 2,
		AnalyzeCache:       cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Build 6 distinct chunks (~6000 runes total) with PII in both the first
	// and last chunks so we verify cached + freshly-scanned offset rebasing.
	var b strings.Builder
	for i := range 6 {
		prefix := fmt.Sprintf("block-%02d ", i)
		switch i {
		case 0:
			prefix += "lead " + ssnA + " "
		case 5:
			prefix += "tail " + ssnB + " "
		}
		b.WriteString(prefix)
		b.WriteString(strings.Repeat(fmt.Sprintf("w%02d ", i), 260))
	}
	text := b.String()

	// Attempt 1: tight budget allows wave 1 (2 chunks, ~45ms) to finish and
	// populate the cache, then times out before all 6 chunks can complete.
	attempt1Ctx := WithAnalyzeTimeout(context.Background(), 75*time.Millisecond)
	if _, err := r.Scrub(attempt1Ctx, text, NewRegistry()); err == nil {
		t.Fatal("expected attempt 1 to fail closed on timeout")
	}
	callsAfterAttempt1 := calls.Load()
	if callsAfterAttempt1 < 2 {
		t.Fatalf("expected at least wave 1 chunks to reach analyzer, got %d calls", callsAfterAttempt1)
	}

	// Attempt 2: already-completed chunks from attempt 1 must be served from
	// cache so fewer Presidio calls are needed to finish the whole field.
	callsBeforeAttempt2 := calls.Load()
	attempt2Ctx := WithAnalyzeTimeout(context.Background(), 500*time.Millisecond)
	res, err := r.Scrub(attempt2Ctx, text, NewRegistry())
	if err != nil {
		t.Fatalf("attempt 2 failed: %v", err)
	}
	callsDuringAttempt2 := calls.Load() - callsBeforeAttempt2
	if callsDuringAttempt2 >= 6 {
		t.Fatalf("expected cached chunks from attempt 1 to skip Presidio; attempt 2 still made %d calls", callsDuringAttempt2)
	}
	if strings.Contains(res.Text, ssnA) || strings.Contains(res.Text, ssnB) {
		t.Fatalf("raw SSN leaked after chunked retry: %q", res.Text)
	}
	if res.EntityCounts["US_SSN"] != 2 {
		t.Fatalf("US_SSN count = %d, want 2", res.EntityCounts["US_SSN"])
	}

	// Attempt 3: full-field cache entry is now warm -> zero Presidio calls.
	callsBeforeAttempt3 := calls.Load()
	if _, err := r.Scrub(attempt2Ctx, text, NewRegistry()); err != nil {
		t.Fatalf("attempt 3 failed: %v", err)
	}
	if delta := calls.Load() - callsBeforeAttempt3; delta != 0 {
		t.Fatalf("attempt 3 made %d Presidio calls, want 0 (warm full-text cache)", delta)
	}
}

func TestAnalyzeChunked_PrefixCacheHitOnAppendedText(t *testing.T) {
	var calls atomic.Int32
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	fp := AnalyzeCacheFingerprint(Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	})
	cache, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
	}, fp)
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            time.Second,
		ChunkChars:         1200,
		AnalyzeConcurrency: 4,
		AnalyzeCache:       cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var b strings.Builder
	for i := range 4 {
		b.WriteString(strings.Repeat(fmt.Sprintf("p%02d ", i), 280))
	}
	base := b.String()

	if _, err := r.Scrub(context.Background(), base, NewRegistry()); err != nil {
		t.Fatalf("initial Scrub: %v", err)
	}
	initialCalls := calls.Load()
	if initialCalls < 3 {
		t.Fatalf("expected multi-chunk initial scan, got %d calls", initialCalls)
	}

	// Append a new suffix: prefix chunks are unchanged and must hit cache.
	extended := base + strings.Repeat("tail ", 260)
	if _, err := r.Scrub(context.Background(), extended, NewRegistry()); err != nil {
		t.Fatalf("extended Scrub: %v", err)
	}
	newCalls := calls.Load() - initialCalls
	if newCalls >= initialCalls {
		t.Fatalf("expected prefix chunks to hit cache; initial=%d, appended=%d", initialCalls, newCalls)
	}
}

func TestAnalyzeLeaf_CoalescesConcurrentIdenticalRequestsAndPreservesSurvivor(t *testing.T) {
	const ssn = "222-33-4444"
	var calls atomic.Int32
	started := make(chan struct{}, 2)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-time.After(120 * time.Millisecond):
		case <-req.Context().Done():
			return
		}
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		idx := strings.Index(payload.Text, ssn)
		_ = json.NewEncoder(w).Encode([]Span{{
			Start: idx, End: idx + len(ssn), EntityType: "US_SSN", Score: 0.95,
		}})
	})

	fp := AnalyzeCacheFingerprint(Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	})
	cache, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
	}, fp)
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}

	r, err := New(Config{
		AnalyzerURL:  srv.URL,
		Timeout:      time.Second,
		AnalyzeCache: cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text := "caller ssn is " + ssn

	// Caller A has a short timeout (40ms) and will abandon early; Caller B
	// joins the same in-flight call with a generous timeout and must succeed
	// without triggering a second Presidio HTTP call.
	var wg sync.WaitGroup
	var errA, errB error
	var resB Result

	wg.Add(2)
	go func() {
		defer wg.Done()
		ctxA := WithAnalyzeTimeout(context.Background(), 40*time.Millisecond)
		_, errA = r.Scrub(ctxA, text, NewRegistry())
	}()
	go func() {
		defer wg.Done()
		<-started
		ctxB := WithAnalyzeTimeout(context.Background(), 500*time.Millisecond)
		resB, errB = r.Scrub(ctxB, text, NewRegistry())
	}()
	wg.Wait()

	if !errors.Is(errA, context.DeadlineExceeded) {
		t.Fatalf("caller A err = %v, want context.DeadlineExceeded", errA)
	}
	if errB != nil {
		t.Fatalf("caller B failed: %v", errB)
	}
	if strings.Contains(resB.Text, ssn) {
		t.Fatalf("caller B leaked raw SSN: %q", resB.Text)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Presidio calls = %d, want 1 (coalesced)", got)
	}

	// Leader must have populated the cache so a subsequent caller hits cache.
	if _, err := r.Scrub(context.Background(), text, NewRegistry()); err != nil {
		t.Fatalf("post-coalesce Scrub: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Presidio calls after cached follow-up = %d, want 1", got)
	}
}

func TestScrubJSON_NestedChunkingRespectsAnalyzeConcurrency(t *testing.T) {
	const limit = 3
	var active atomic.Int32
	var peak atomic.Int32

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		cur := active.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		active.Add(-1)
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            2 * time.Second,
		ChunkChars:         1100,
		AnalyzeConcurrency: limit,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 4 messages, each ~3600 chars (4 distinct chunks) => 16 total chunks.
	// Without the shared per-request semaphore, nested fan-out would reach 3*3=9.
	msgs := make([]any, 4)
	for m := range msgs {
		var b strings.Builder
		for c := range 4 {
			b.WriteString(strings.Repeat(fmt.Sprintf("m%dc%d ", m, c), 200))
		}
		msgs[m] = map[string]any{"role": "user", "content": b.String()}
	}
	body, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ctx := WithProvider(context.Background(), "openai")
	if _, err := r.Scrub(ctx, string(body), NewRegistry()); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("peak concurrent Presidio calls = %d, want <= %d", got, limit)
	}
	if got := peak.Load(); got < 2 {
		t.Fatalf("expected parallel execution, peak = %d", got)
	}
}

func TestScrubJSON_SharesOverallDeadlineAcrossMultipleFields(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-time.After(70 * time.Millisecond):
		case <-req.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            130 * time.Millisecond,
		AnalyzeConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"messages":[` +
		`{"role":"user","content":"first distinct message"},` +
		`{"role":"user","content":"second distinct message"},` +
		`{"role":"user","content":"third distinct message"},` +
		`{"role":"user","content":"fourth distinct message"}` +
		`]}`
	ctx := WithProvider(context.Background(), "openai")
	ctx = WithAnalyzeTimeout(ctx, 130*time.Millisecond)

	start := time.Now()
	_, err = r.Scrub(ctx, body, NewRegistry())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected shared deadline timeout, got nil in %v", elapsed)
	}
	if elapsed > 260*time.Millisecond {
		t.Fatalf("took %v; scrubJSON should share one 130ms deadline across fields", elapsed)
	}
}

func TestNew_DefaultHTTPClientRaisesMaxIdleConnsPerHost(t *testing.T) {
	r, err := New(Config{AnalyzerURL: "http://localhost:3000", AnalyzeConcurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr, ok := r.client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected *http.Transport, got %T", r.client.Transport)
	}
	if tr == http.DefaultTransport {
		t.Fatal("default transport must be cloned, not mutated in place")
	}
	if tr.MaxIdleConnsPerHost < 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= 16", tr.MaxIdleConnsPerHost)
	}
}
