package redact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
)

// TestAnalyzeChunked_PerChunkCacheSurvivesPartialTimeoutAndAcceleratesRetry
// reproduces the Nov 24 2026 ~400 KB fail-closed retry loop: when a large
// multi-chunk field aborts in a later wave, chunks that already completed must
// stay cached so a retry scans only the remaining chunks instead of repeating
// the exact same failure indefinitely.
func TestAnalyzeChunked_PerChunkCacheSurvivesPartialTimeoutAndAcceleratesRetry(t *testing.T) {
	const (
		ssnA = "111-22-3333"
		ssnB = "444-55-6666"
	)

	var calls atomic.Int32
	var attempt1Active atomic.Bool
	attempt1Active.Store(true)
	attempt1Ctx, attempt1Cancel := context.WithCancel(context.Background())
	defer attempt1Cancel()

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		n := calls.Add(1)
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// During attempt 1, let the first 2 chunks succeed and cache, then block
		// chunk 3+ until attempt 1 cancels so the partial-completion boundary
		// is deterministic under CPU load.
		if attempt1Active.Load() && n > 2 {
			attempt1Cancel()
			<-req.Context().Done()
			return
		}

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
	totalChunks := len(chunkRunes([]rune(text), 1200, analyzeChunkOverlapChars))
	if totalChunks < 4 {
		t.Fatalf("expected at least 4 chunks, got %d", totalChunks)
	}

	// Attempt 1: with AnalyzeConcurrency=2, request #3 cannot even begin until
	// at least one worker has finished its first chunk and written L1 cache.
	if _, err := r.Scrub(attempt1Ctx, text, NewRegistry()); err == nil {
		t.Fatal("expected attempt 1 to fail closed on cancellation")
	}
	if got := calls.Load(); got < 3 {
		t.Fatalf("expected wave 1 + wave 2 trigger to reach analyzer, got %d calls", got)
	}

	// Attempt 2: already-completed chunks from attempt 1 must be served from
	// cache so fewer Presidio calls than totalChunks are needed to finish.
	attempt1Active.Store(false)
	callsBeforeAttempt2 := calls.Load()
	attempt2Ctx := WithAnalyzeTimeout(context.Background(), time.Second)
	res, err := r.Scrub(attempt2Ctx, text, NewRegistry())
	if err != nil {
		t.Fatalf("attempt 2 failed: %v", err)
	}
	callsDuringAttempt2 := calls.Load() - callsBeforeAttempt2
	if callsDuringAttempt2 >= int32(totalChunks) {
		t.Fatalf("expected cached chunks from attempt 1 to skip Presidio; attempt 2 made %d calls for %d total chunks", callsDuringAttempt2, totalChunks)
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

func TestAnalyzeChunked_PerChunkCacheWithRedisBackend(t *testing.T) {
	mr := miniredis.RunT(t)
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
	makeRedactor := func() (*Redactor, func() error) {
		cache, closeFn, err := NewAnalyzeCache(AnalyzeCacheConfig{
			Enabled:       true,
			TTL:           time.Minute,
			MemoryEnabled: false,
			RedisEnabled:  true,
			Redis:         &config.RedisConfig{Address: mr.Addr()},
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
		return r, closeFn
	}

	r1, close1 := makeRedactor()
	defer func() { _ = close1() }()
	r2, close2 := makeRedactor()
	defer func() { _ = close2() }()

	var b strings.Builder
	for i := range 4 {
		b.WriteString(strings.Repeat(fmt.Sprintf("rc%02d ", i), 280))
	}
	base := b.String()

	if _, err := r1.Scrub(context.Background(), base, NewRegistry()); err != nil {
		t.Fatalf("r1 Scrub: %v", err)
	}
	initialCalls := calls.Load()
	if initialCalls < 3 {
		t.Fatalf("expected multi-chunk scan, got %d calls", initialCalls)
	}

	// Wait briefly for r1's async Redis chunk writes to land in miniredis.
	deadline := time.Now().Add(time.Second)
	for len(mr.Keys()) < int(initialCalls) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	// r2 has no in-process memory layer; appended text should hit r1's prefix
	// chunks in Redis and only scan the new tail chunk.
	extended := base + strings.Repeat("tail ", 260)
	if _, err := r2.Scrub(context.Background(), extended, NewRegistry()); err != nil {
		t.Fatalf("r2 Scrub: %v", err)
	}
	newCalls := calls.Load() - initialCalls
	if newCalls >= initialCalls {
		t.Fatalf("expected Redis per-chunk cache hit on prefix; initial=%d, appended=%d", initialCalls, newCalls)
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

func TestAnalyzeLeaf_AllWaitersCanceledCancelsUpstream(t *testing.T) {
	var startOnce sync.Once
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{}, 1)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		startOnce.Do(func() { close(started) })
		<-req.Context().Done()
		select {
		case upstreamCanceled <- struct{}{}:
		default:
		}
	})

	r, err := New(Config{
		AnalyzerURL: srv.URL,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = r.Scrub(ctxA, "shared cancel text", NewRegistry())
	}()
	go func() {
		defer wg.Done()
		<-started
		_, _ = r.Scrub(ctxB, "shared cancel text", NewRegistry())
	}()

	<-started
	time.Sleep(15 * time.Millisecond)
	cancelA()
	cancelB()
	wg.Wait()

	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("upstream Presidio request was not canceled after all waiters abandoned it")
	}
}

func TestAnalyzeLeaf_LeaderErrorFansOutAndDoesNotPoisonNextRequest(t *testing.T) {
	var calls atomic.Int32
	var startOnce sync.Once
	started := make(chan struct{})
	release := make(chan struct{})

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			startOnce.Do(func() { close(started) })
			<-release
			http.Error(w, "sidecar error", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{
		AnalyzerURL: srv.URL,
		Timeout:     time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errA = r.Scrub(context.Background(), "shared error text", NewRegistry())
	}()
	go func() {
		defer wg.Done()
		<-started
		_, errB = r.Scrub(context.Background(), "shared error text", NewRegistry())
	}()

	<-started
	time.Sleep(15 * time.Millisecond)
	close(release)
	wg.Wait()

	if errA == nil || errB == nil {
		t.Fatalf("expected error fan-out to both callers; errA=%v errB=%v", errA, errB)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 coalesced failed call, got %d", got)
	}

	// Subsequent call must start fresh and succeed (no stale inflight entry).
	if _, err := r.Scrub(context.Background(), "shared error text", NewRegistry()); err != nil {
		t.Fatalf("subsequent Scrub after leader error failed: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 total calls after recovery, got %d", got)
	}
}

// TestAnalyzeLeaf_HungLeaderBoundedAndFreshWaiterSucceeds verifies P1-3: a hung
// coalesced Presidio call never runs unbounded and stops accepting new waiters
// once half their budget has elapsed, letting fresh arrivals hit a healthy
// worker while the hung call cancels cleanly once its waiters leave.
func TestAnalyzeLeaf_HungLeaderBoundedAndFreshWaiterSucceeds(t *testing.T) {
	const ssn = "333-44-5555"
	var calls atomic.Int32
	call1Started := make(chan struct{}, 1)
	call1Canceled := make(chan struct{}, 1)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			_, _ = io.Copy(io.Discard, req.Body)
			select {
			case call1Started <- struct{}{}:
			default:
			}
			<-req.Context().Done()
			select {
			case call1Canceled <- struct{}{}:
			default:
			}
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

	r, err := New(Config{
		AnalyzerURL: srv.URL,
		Timeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text := "shared system prompt " + ssn
	const budget = 120 * time.Millisecond

	var wg sync.WaitGroup
	var err1, err2, err3 error
	var res3 Result

	// Waiter 1 starts the hung call.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := WithAnalyzeTimeout(context.Background(), budget)
		_, err1 = r.Scrub(ctx, text, NewRegistry())
	}()

	<-call1Started

	// Waiter 2 arrives quickly (< budget/2) and coalesces onto call 1.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := WithAnalyzeTimeout(context.Background(), budget)
		_, err2 = r.Scrub(ctx, text, NewRegistry())
	}()

	// Wait past budget/2 so call 1 is too stale for Waiter 3 to join; Waiter 3
	// starts call 2 on a healthy worker and must succeed.
	time.Sleep(budget/2 + 20*time.Millisecond)
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := WithAnalyzeTimeout(context.Background(), budget)
		res3, err3 = r.Scrub(ctx, text, NewRegistry())
	}()

	wg.Wait()

	if !errors.Is(err1, context.DeadlineExceeded) {
		t.Fatalf("waiter 1 err = %v, want context.DeadlineExceeded", err1)
	}
	if !errors.Is(err2, context.DeadlineExceeded) {
		t.Fatalf("waiter 2 err = %v, want context.DeadlineExceeded", err2)
	}
	if err3 != nil {
		t.Fatalf("waiter 3 should have started a fresh call and succeeded, got: %v", err3)
	}
	if strings.Contains(res3.Text, ssn) {
		t.Fatalf("waiter 3 leaked raw SSN: %q", res3.Text)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Presidio calls = %d, want 2 (1 hung + 1 fresh)", got)
	}

	select {
	case <-call1Canceled:
	case <-time.After(time.Second):
		t.Fatal("hung call 1 was never canceled after its waiters timed out")
	}
}

// TestAnalyzeLeaf_FollowerDoesNotQueueBehindLeaderSemaphore verifies P2-3:
// when Request A's semaphore is saturated by a slow block, an independent
// Request B asking for a shared block does not wait behind Request A's
// semaphore queue.
func TestAnalyzeLeaf_FollowerDoesNotQueueBehindLeaderSemaphore(t *testing.T) {
	slowStarted := make(chan struct{}, 1)
	releaseSlow := make(chan struct{})

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		if strings.Contains(payload.Text, "slow-block") {
			select {
			case slowStarted <- struct{}{}:
			default:
			}
			select {
			case <-releaseSlow:
			case <-req.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            time.Second,
		AnalyzeConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Request A occupies its own AnalyzeConcurrency=1 semaphore with slow-block
	// while its second message ("shared-system-prompt") is queued behind it.
	reqABody := `{"messages":[{"role":"user","content":"slow-block"},{"role":"user","content":"shared-system-prompt"}]}`
	reqADone := make(chan error, 1)
	go func() {
		ctx := WithProvider(context.Background(), "openai")
		_, err := r.Scrub(ctx, reqABody, NewRegistry())
		reqADone <- err
	}()

	<-slowStarted

	// Request B arrives independently for "shared-system-prompt" with a tight
	// deadline while Request A is still blocked on slow-block.
	ctxB := WithAnalyzeTimeout(context.Background(), 150*time.Millisecond)
	start := time.Now()
	if _, err := r.Scrub(ctxB, "shared-system-prompt", NewRegistry()); err != nil {
		close(releaseSlow)
		<-reqADone
		t.Fatalf("request B failed while request A held its semaphore: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 120*time.Millisecond {
		close(releaseSlow)
		<-reqADone
		t.Fatalf("request B took %v, expected immediate completion without waiting on request A", elapsed)
	}

	close(releaseSlow)
	if err := <-reqADone; err != nil {
		t.Fatalf("request A failed: %v", err)
	}
}

type blockingSetCache struct {
	setEntered  chan struct{}
	setCanceled chan struct{}
}

func (c *blockingSetCache) Get(_ context.Context, _ string) ([]Span, bool) { return nil, false }
func (c *blockingSetCache) GetMulti(_ context.Context, _ []string) map[string][]Span {
	return nil
}

func (c *blockingSetCache) Set(ctx context.Context, _ string, _ []Span) {
	select {
	case c.setEntered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case c.setCanceled <- struct{}{}:
	default:
	}
}

// TestAnalyzeLeaf_SlowCacheSetDoesNotBlockWaiters verifies P1-2: a stalled
// remote cache Set must run off the waiter-release critical path and respect a
// bounded write timeout.
func TestAnalyzeLeaf_SlowCacheSetDoesNotBlockWaiters(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	cache := &blockingSetCache{
		setEntered:  make(chan struct{}, 1),
		setCanceled: make(chan struct{}, 1),
	}
	r, err := New(Config{
		AnalyzerURL:  srv.URL,
		Timeout:      time.Second,
		AnalyzeCache: cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := WithAnalyzeTimeout(context.Background(), 150*time.Millisecond)
	start := time.Now()
	if _, err := r.Scrub(ctx, "hello blocking cache", NewRegistry()); err != nil {
		t.Fatalf("Scrub failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Scrub took %v; waiters must not block on slow AnalyzeCache.Set", elapsed)
	}

	select {
	case <-cache.setEntered:
	case <-time.After(time.Second):
		t.Fatal("expected async AnalyzeCache.Set to run")
	}
	select {
	case <-cache.setCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async AnalyzeCache.Set context to expire under bounded timeout")
	}
}

// TestScrubJSON_StalledRedisDoesNotFailClosed reproduces P1-2: a stalled Redis
// socket (accepted TCP connection that stops responding) must not consume the
// whole-body Presidio budget or turn a healthy Presidio scan into a timeout/503.
func TestScrubJSON_StalledRedisDoesNotFailClosed(t *testing.T) {
	const ssn = "555-66-7777"
	mr := miniredis.RunT(t)

	// Start a TCP proxy in front of miniredis: pass traffic through during
	// NewAnalyzeCache startup Ping, then stall all further reads on both
	// existing pooled connections and newly dialed connections.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var stalled atomic.Bool
	proxyDone := make(chan struct{})
	var proxyWG sync.WaitGroup
	proxyWG.Add(1)
	go func() {
		defer proxyWG.Done()
		for {
			clientConn, err := ln.Accept()
			if err != nil {
				return
			}
			proxyWG.Add(1)
			go func(cc net.Conn) {
				defer proxyWG.Done()
				defer func() { _ = cc.Close() }()
				if stalled.Load() {
					<-proxyDone
					return
				}
				upConn, err := net.Dial("tcp", mr.Addr())
				if err != nil {
					return
				}
				defer func() { _ = upConn.Close() }()
				go func() { _, _ = io.Copy(cc, upConn) }()
				buf := make([]byte, 4096)
				for {
					n, err := cc.Read(buf)
					if err != nil {
						return
					}
					if stalled.Load() {
						// Socket stays open without replying, simulating a stalled Redis.
						<-proxyDone
						return
					}
					if _, err := upConn.Write(buf[:n]); err != nil {
						return
					}
				}
			}(clientConn)
		}
	}()
	t.Cleanup(func() {
		close(proxyDone)
		_ = ln.Close()
		proxyWG.Wait()
	})

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		var spans []Span
		if idx := strings.Index(payload.Text, ssn); idx >= 0 {
			spans = append(spans, Span{
				Start: idx, End: idx + len(ssn), EntityType: "US_SSN", Score: 0.95,
			})
		}
		_ = json.NewEncoder(w).Encode(spans)
	})

	fp := AnalyzeCacheFingerprint(Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	})
	cache, closeCache, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
		RedisEnabled:  true,
		Redis:         &config.RedisConfig{Address: ln.Addr().String()},
	}, fp)
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}
	defer func() { _ = closeCache() }()

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            time.Second,
		AnalyzeConcurrency: 4,
		AnalyzeCache:       cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Now stall the pooled Redis connection before running Scrub.
	stalled.Store(true)

	body := `{"messages":[` +
		`{"role":"user","content":"first message with ` + ssn + `"},` +
		`{"role":"user","content":"second distinct message"},` +
		`{"role":"user","content":"third distinct message"},` +
		`{"role":"user","content":"fourth distinct message"}` +
		`]}`
	ctx := WithProvider(context.Background(), "openai")
	ctx = WithAnalyzeTimeout(ctx, 500*time.Millisecond)

	start := time.Now()
	res, err := r.Scrub(ctx, body, NewRegistry())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Scrub failed under stalled Redis after %v: %v", elapsed, err)
	}
	if strings.Contains(res.Text, ssn) {
		t.Fatalf("raw SSN leaked under stalled Redis: %q", res.Text)
	}
	// Prefetch times out quickly (~150ms), Presidio succeeds instantly, and
	// remote Redis Set runs off the critical path -> total latency stays low.
	if elapsed > 600*time.Millisecond {
		t.Fatalf("Scrub took %v under stalled Redis; expected fast fail-over to Presidio", elapsed)
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
