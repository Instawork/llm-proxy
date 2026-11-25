package redact

import (
	"context"
	"crypto/sha256"
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

	// Attempt 2 must also populate the merged full-text cache entry so attempt 3
	// hits in a single lookup without re-chunking.
	if cachedFull, ok := cache.Get(context.Background(), text); !ok || len(cachedFull) != 2 {
		t.Fatalf("merged full-text cache entry missing after attempt 2: ok=%v len=%d", ok, len(cachedFull))
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

func TestAnalyzeCacheFingerprint_IncludesChunkChars(t *testing.T) {
	base := Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
		ChunkChars:     32768,
	}
	other := base
	other.ChunkChars = 65536
	if AnalyzeCacheFingerprint(base) == AnalyzeCacheFingerprint(other) {
		t.Fatal("AnalyzeCacheFingerprint must differ when ChunkChars differs")
	}
}

func TestInflightCanJoin_RejectsShorterRemainingLifetime(t *testing.T) {
	now := time.Now()
	shortCall := &inflightAnalyzeCall{
		startedAt: now.Add(-10 * time.Millisecond),
		deadline:  now.Add(90 * time.Millisecond),
	}
	// A 1000ms waiter must not join a call with only 90ms left (< 500ms).
	if shortCall.canJoin(now, time.Second) {
		t.Fatal("expected long-budget waiter to reject short-remaining-lifetime call")
	}
	// A 100ms waiter with 10ms elapsed (< 50ms) and 90ms left (>= 50ms) joins.
	if !shortCall.canJoin(now, 100*time.Millisecond) {
		t.Fatal("expected equal-budget waiter to join fresh call")
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

func waitSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitInflightWaiters(t *testing.T, r *Redactor, text string, want int) {
	t.Helper()
	key := sha256.Sum256([]byte(text))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.inflightMu.Lock()
		call := r.inflight[key]
		got := 0
		if call != nil {
			got = call.waiters
		}
		r.inflightMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for inflight waiter count == %d", want)
}

func TestAnalyzeLeaf_CoalescesConcurrentIdenticalRequestsAndPreservesSurvivor(t *testing.T) {
	const ssn = "222-33-4444"
	var calls atomic.Int32
	var startOnce sync.Once
	started := make(chan struct{})
	var releaseOnce sync.Once
	release := make(chan struct{})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
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

	// Caller A starts the call, Caller B attaches deterministically, then A
	// cancels early; B must survive and succeed without a second HTTP call.
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	doneA := make(chan error, 1)
	go func() {
		_, err := r.Scrub(ctxA, text, NewRegistry())
		doneA <- err
	}()

	waitSignal(t, started, "caller A analyzer request")

	type scrubOutcome struct {
		res Result
		err error
	}
	doneB := make(chan scrubOutcome, 1)
	go func() {
		ctxB := WithAnalyzeTimeout(context.Background(), time.Second)
		res, err := r.Scrub(ctxB, text, NewRegistry())
		doneB <- scrubOutcome{res: res, err: err}
	}()

	waitInflightWaiters(t, r, text, 2)
	cancelA()

	select {
	case errA := <-doneA:
		if !errors.Is(errA, context.Canceled) {
			t.Fatalf("caller A err = %v, want context.Canceled", errA)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for caller A to exit after cancel")
	}

	releaseOnce.Do(func() { close(release) })

	var resB Result
	select {
	case outB := <-doneB:
		if outB.err != nil {
			t.Fatalf("caller B failed: %v", outB.err)
		}
		resB = outB.res
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for caller B to succeed")
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

	const text = "shared cancel text"
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelA()
	defer cancelB()

	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() {
		_, err := r.Scrub(ctxA, text, NewRegistry())
		doneA <- err
	}()
	waitSignal(t, started, "upstream request start")

	go func() {
		_, err := r.Scrub(ctxB, text, NewRegistry())
		doneB <- err
	}()
	waitInflightWaiters(t, r, text, 2)

	// Canceling only caller A must NOT cancel upstream while B is still attached.
	cancelA()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for caller A to exit")
	}
	waitInflightWaiters(t, r, text, 1)
	select {
	case <-upstreamCanceled:
		t.Fatal("upstream canceled prematurely while caller B was still waiting")
	default:
	}

	// Canceling the last waiter (B) must cancel upstream promptly.
	cancelB()
	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for caller B to exit")
	}
	waitSignal(t, upstreamCanceled, "upstream cancellation after all waiters left")
}

func TestAnalyzeLeaf_LeaderErrorFansOutAndDoesNotPoisonNextRequest(t *testing.T) {
	var calls atomic.Int32
	var startOnce sync.Once
	started := make(chan struct{})
	var releaseOnce sync.Once
	release := make(chan struct{})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

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

	const text = "shared error text"
	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errA = r.Scrub(context.Background(), text, NewRegistry())
	}()
	waitSignal(t, started, "leader error request start")

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errB = r.Scrub(context.Background(), text, NewRegistry())
	}()
	waitInflightWaiters(t, r, text, 2)

	releaseOnce.Do(func() { close(release) })
	wg.Wait()

	if errA == nil || errB == nil {
		t.Fatalf("expected error fan-out to both callers; errA=%v errB=%v", errA, errB)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 coalesced failed call, got %d", got)
	}

	// Subsequent call must start fresh and succeed (no stale inflight entry).
	if _, err := r.Scrub(context.Background(), text, NewRegistry()); err != nil {
		t.Fatalf("subsequent Scrub after leader error failed: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 total calls after recovery, got %d", got)
	}
}

// TestAnalyzeLeaf_HungLeaderBoundedAndFreshWaiterSucceeds verifies P1-3: a hung
// coalesced Presidio call stops accepting new waiters once half their budget
// has elapsed even while earlier waiters remain attached, letting fresh
// arrivals hit a healthy worker while the hung call cancels cleanly once its
// waiters leave.
func TestAnalyzeLeaf_HungLeaderBoundedAndFreshWaiterSucceeds(t *testing.T) {
	const ssn = "333-44-5555"
	var calls atomic.Int32
	var call1StartOnce sync.Once
	call1Started := make(chan struct{})
	call1Canceled := make(chan struct{}, 1)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			_, _ = io.Copy(io.Discard, req.Body)
			call1StartOnce.Do(func() { close(call1Started) })
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
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel1()
	defer cancel2()

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() {
		_, err := r.Scrub(ctx1, text, NewRegistry())
		done1 <- err
	}()
	waitSignal(t, call1Started, "hung call 1 start")

	go func() {
		_, err := r.Scrub(ctx2, text, NewRegistry())
		done2 <- err
	}()
	waitInflightWaiters(t, r, text, 2)

	// Age call 1 past Waiter 3's half-budget while Waiters 1 and 2 are STILL
	// attached, proving stale-call rejection on a live in-flight call.
	key := sha256.Sum256([]byte(text))
	r.inflightMu.Lock()
	r.inflight[key].startedAt = time.Now().Add(-200 * time.Millisecond)
	r.inflightMu.Unlock()

	ctx3 := WithAnalyzeTimeout(context.Background(), 200*time.Millisecond)
	res3, err3 := r.Scrub(ctx3, text, NewRegistry())
	if err3 != nil {
		t.Fatalf("waiter 3 should have bypassed stale live call 1 and succeeded, got: %v", err3)
	}
	if strings.Contains(res3.Text, ssn) {
		t.Fatalf("waiter 3 leaked raw SSN: %q", res3.Text)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Presidio calls = %d, want 2 (1 hung + 1 fresh)", got)
	}

	// Now abandon Waiters 1 and 2; even though call 1 was superseded in the
	// inflight map, dropping its waiter count to 0 must cancel call 1 upstream.
	cancel1()
	cancel2()
	if err1 := <-done1; !errors.Is(err1, context.Canceled) {
		t.Fatalf("waiter 1 err = %v, want context.Canceled", err1)
	}
	if err2 := <-done2; !errors.Is(err2, context.Canceled) {
		t.Fatalf("waiter 2 err = %v, want context.Canceled", err2)
	}
	waitSignal(t, call1Canceled, "hung call 1 cancellation after waiters left")
}

// TestAnalyzeLeaf_FollowerDoesNotQueueBehindLeaderSemaphore verifies P2-3:
// when Request A's semaphore is saturated by a slow block, an independent
// Request B asking for a shared block does not wait behind Request A's
// semaphore queue.
func TestAnalyzeLeaf_FollowerDoesNotQueueBehindLeaderSemaphore(t *testing.T) {
	var slowStartOnce sync.Once
	slowStarted := make(chan struct{})
	var releaseOnce sync.Once
	releaseSlow := make(chan struct{})
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSlow) }) })

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		if strings.Contains(payload.Text, "slow-block") {
			slowStartOnce.Do(func() { close(slowStarted) })
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

	waitSignal(t, slowStarted, "request A slow-block start")

	// Request B arrives independently for "shared-system-prompt" with a tight
	// deadline while Request A is still blocked on slow-block.
	ctxB := WithAnalyzeTimeout(context.Background(), 150*time.Millisecond)
	start := time.Now()
	if _, err := r.Scrub(ctxB, "shared-system-prompt", NewRegistry()); err != nil {
		releaseOnce.Do(func() { close(releaseSlow) })
		<-reqADone
		t.Fatalf("request B failed while request A held its semaphore: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 120*time.Millisecond {
		releaseOnce.Do(func() { close(releaseSlow) })
		<-reqADone
		t.Fatalf("request B took %v, expected immediate completion without waiting on request A", elapsed)
	}

	releaseOnce.Do(func() { close(releaseSlow) })
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
	var stalledCmds atomic.Int32
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
					stalledCmds.Add(1)
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
						stalledCmds.Add(1)
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
	if stalledCmds.Load() == 0 {
		t.Fatal("expected Redis proxy to stall at least one Redis command during Scrub")
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
	var completed atomic.Int32
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		select {
		case <-time.After(70 * time.Millisecond):
		case <-req.Context().Done():
			return
		}
		completed.Add(1)
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
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded (elapsed %v)", err, elapsed)
	}
	if got := completed.Load(); got < 1 || got >= 4 {
		t.Fatalf("completed fields = %d, want at least 1 and fewer than 4", got)
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
