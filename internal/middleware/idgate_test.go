package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/redact"
)

type fakeOCR struct {
	text string
	err  error
}

func (f *fakeOCR) ExtractText(_ context.Context, _ []byte, _ string) (string, error) {
	return f.text, f.err
}

type fakeIDAnalyzer struct {
	spans []redact.Span
	err   error
}

func (f *fakeIDAnalyzer) AnalyzeEntities(_ context.Context, _ string, _ []string) ([]redact.Span, error) {
	return f.spans, f.err
}

func TestIDGateMiddleware_NoImagePasses(t *testing.T) {
	cap := &captureHandler{}
	mw := IDGateMiddleware(&fakeOCR{}, &fakeIDAnalyzer{}, IDGateConfig{})(cap)

	body := `{"messages":[{"role":"user","content":"hello"}]}`
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if cap.reqSeen == nil {
		t.Fatal("expected downstream handler")
	}
}

func TestIDGateMiddleware_BlocksGovID(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	cap := &captureHandler{}
	mw := IDGateMiddleware(
		&fakeOCR{text: "PASSPORT 123456789"},
		&fakeIDAnalyzer{spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}}},
		IDGateConfig{ScoreThreshold: 0.6},
	)(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if rec.Body.String() != proxylog.ProxyMsg(idGateBlockMessage)+"\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if cap.reqSeen != nil {
		t.Fatal("downstream handler should not run")
	}
}

func TestIDGateMiddleware_PassBelowThreshold(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	cap := &captureHandler{}
	mw := IDGateMiddleware(
		&fakeOCR{text: "maybe passport"},
		&fakeIDAnalyzer{spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.4}}},
		IDGateConfig{ScoreThreshold: 0.6},
	)(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if cap.reqSeen == nil {
		t.Fatal("expected downstream handler")
	}
}

func TestIDGateMiddleware_OCRErrorFailOpen(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	cap := &captureHandler{}
	mw := IDGateMiddleware(
		&fakeOCR{err: errors.New("ocr down")},
		&fakeIDAnalyzer{},
		IDGateConfig{FailClosed: false},
	)(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if cap.reqSeen == nil {
		t.Fatal("expected downstream handler on fail_open")
	}
}

func TestIDGateMiddleware_OCRErrorFailClosed(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("jpeg-bytes"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	cap := &captureHandler{}
	mw := IDGateMiddleware(
		&fakeOCR{err: errors.New("ocr down")},
		&fakeIDAnalyzer{},
		IDGateConfig{FailClosed: true},
	)(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if cap.reqSeen != nil {
		t.Fatal("downstream handler should not run")
	}
}

func TestExtractImagesFromBody_AnthropicAndGemini(t *testing.T) {
	img := base64.StdEncoding.EncodeToString([]byte("img"))
	body := []byte(`{
		"messages":[{"content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + img + `"}}]}],
		"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + img + `"}}]}]
	}`)
	images, err := extractImagesFromBody(body, 1024*1024)
	if err != nil {
		t.Fatalf("extractImagesFromBody: %v", err)
	}
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}
}

func TestGovIDHit(t *testing.T) {
	spans := []redact.Span{{EntityType: "US_DRIVER_LICENSE", Score: 0.61}}
	ok, entity, score := govIDHit(spans, redact.DefaultGovIDEntityTypes, 0.6)
	if !ok || entity != "US_DRIVER_LICENSE" || score != 0.61 {
		t.Fatalf("got (%v, %q, %v)", ok, entity, score)
	}
	ok, _, _ = govIDHit([]redact.Span{{EntityType: "US_PASSPORT", Score: 0.4}}, redact.DefaultGovIDEntityTypes, 0.4)
	if !ok {
		t.Fatal("expected score 0.4 to block at threshold 0.4")
	}
}

type funcOCR struct {
	fn func(ctx context.Context, img []byte, filename string) (string, error)
}

func (f *funcOCR) ExtractText(ctx context.Context, img []byte, filename string) (string, error) {
	return f.fn(ctx, img, filename)
}

func TestIDGateMiddleware_DeduplicatesWithinRequest(t *testing.T) {
	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			calls.Add(1)
			return "plain receipt", nil
		},
	}

	img := base64.StdEncoding.EncodeToString([]byte("duplicate-img"))
	body := fmt.Sprintf(
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
		img, img, img,
	)

	cap := &captureHandler{}
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{})(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if cap.reqSeen == nil {
		t.Fatal("expected downstream handler")
	}
	if calls.Load() != 1 {
		t.Fatalf("ocr calls = %d, want 1", calls.Load())
	}
}

func TestIDGateMiddleware_CachesClearAndBlockedVerdicts(t *testing.T) {
	var clearCalls atomic.Int32
	clearOCR := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			clearCalls.Add(1)
			return "hello world", nil
		},
	}
	clearMW := IDGateMiddleware(clearOCR, &fakeIDAnalyzer{}, IDGateConfig{CacheTTL: time.Minute})(&captureHandler{})

	img := base64.StdEncoding.EncodeToString([]byte("cached-clear-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`
	clearMW.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	clearMW.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if clearCalls.Load() != 1 {
		t.Fatalf("clear ocr calls = %d, want 1", clearCalls.Load())
	}

	var blockCalls atomic.Int32
	blockOCR := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			blockCalls.Add(1)
			return "PASSPORT 123456789", nil
		},
	}
	blockMW := IDGateMiddleware(
		blockOCR,
		&fakeIDAnalyzer{spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}}},
		IDGateConfig{ScoreThreshold: 0.6, CacheTTL: time.Minute},
	)(&captureHandler{})

	idImg := base64.StdEncoding.EncodeToString([]byte("cached-passport-img"))
	idBody := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + idImg + `"}}]}]}`

	rec1 := httptest.NewRecorder()
	blockMW.ServeHTTP(rec1, newReq(t, http.MethodPost, "/openai/v1/chat/completions", idBody))
	rec2 := httptest.NewRecorder()
	blockMW.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", idBody))

	if rec1.Code != http.StatusUnprocessableEntity || rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("statuses = (%d, %d), want (422, 422)", rec1.Code, rec2.Code)
	}
	if blockCalls.Load() != 1 {
		t.Fatalf("block ocr calls = %d, want 1", blockCalls.Load())
	}
}

func TestIDGateMiddleware_DoesNotCacheErrors(t *testing.T) {
	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			if calls.Add(1) == 1 {
				return "", errors.New("temporary timeout")
			}
			return "PASSPORT 123456789", nil
		},
	}

	mw := IDGateMiddleware(
		ocrClient,
		&fakeIDAnalyzer{spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}}},
		IDGateConfig{FailClosed: false, ScoreThreshold: 0.6, CacheTTL: time.Minute},
	)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	img := base64.StdEncoding.EncodeToString([]byte("flaky-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200 (fail_open)", rec1.Code)
	}

	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second request status = %d, want 422 after retry", rec2.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestIDGateMiddleware_CoalescesConcurrentIdenticalImages(t *testing.T) {
	const n = 8
	leaderStarted := make(chan struct{})
	releaseLeader := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseLeader) }) }
	followersJoined := make(chan struct{}, n-1)

	prevHook := idGateTestOnInflightWait
	idGateTestOnInflightWait = func() { followersJoined <- struct{}{} }
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	var wg sync.WaitGroup
	t.Cleanup(func() {
		release()
		wg.Wait()
	})

	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			if calls.Add(1) == 1 {
				close(leaderStarted)
				<-releaseLeader
			}
			return "receipt text", nil
		},
	}
	// CacheTTL == 0 disables completed-result caching so only true in-flight
	// coalescing can keep OCR calls at 1.
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{CacheTTL: 0})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	img := base64.StdEncoding.EncodeToString([]byte("shared-burst-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		if rec.Code != http.StatusOK {
			t.Errorf("leader status = %d, want 200", rec.Code)
		}
	}()
	select {
	case <-leaderStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for leader to start OCR")
	}

	for range n - 1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
			if rec.Code != http.StatusOK {
				t.Errorf("follower status = %d, want 200", rec.Code)
			}
		}()
	}

	joinTimer := time.NewTimer(5 * time.Second)
	defer joinTimer.Stop()
	for i := range n - 1 {
		select {
		case <-followersJoined:
		case <-joinTimer.C:
			t.Fatalf("timed out waiting for follower %d/%d to join in-flight scan", i+1, n-1)
		}
	}
	release()
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("concurrent ocr calls = %d, want 1", calls.Load())
	}
}

func TestIDGateMiddleware_CoalescedLeaderCancelDoesNotPoisonFollower(t *testing.T) {
	leaderStarted := make(chan struct{})
	followerJoined := make(chan struct{}, 1)

	prevHook := idGateTestOnInflightWait
	idGateTestOnInflightWait = func() { followerJoined <- struct{}{} }
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancelLeader()
		wg.Wait()
	})

	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(ctx context.Context, _ []byte, _ string) (string, error) {
			n := calls.Add(1)
			if n == 1 {
				close(leaderStarted)
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "clean text", nil
		},
	}

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		FailClosed: true,
		CacheTTL:   time.Minute,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	img := base64.StdEncoding.EncodeToString([]byte("leader-cancel-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	leaderReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body).WithContext(leaderCtx)
	followerReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)

	wg.Add(1)
	go func() {
		defer wg.Done()
		mw.ServeHTTP(httptest.NewRecorder(), leaderReq)
	}()

	select {
	case <-leaderStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for leader to start OCR")
	}
	followerRec := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		mw.ServeHTTP(followerRec, followerReq)
	}()

	select {
	case <-followerJoined:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for follower to join in-flight scan")
	}
	cancelLeader()
	wg.Wait()

	if followerRec.Code != http.StatusOK {
		t.Fatalf("follower status = %d, want 200 (must retry when leader cancels)", followerRec.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestIDGateMiddleware_CoalescedLeaderDeadlineDoesNotPoisonFollower(t *testing.T) {
	prevHook := idGateTestOnInflightWait
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	synctest.Test(t, func(t *testing.T) {
		leaderStarted := make(chan struct{})
		followerJoined := make(chan struct{}, 1)
		idGateTestOnInflightWait = func() { followerJoined <- struct{}{} }

		var calls atomic.Int32
		ocrClient := &funcOCR{
			fn: func(ctx context.Context, _ []byte, _ string) (string, error) {
				if calls.Add(1) == 1 {
					close(leaderStarted)
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "PASSPORT 123456789", nil
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{
			spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}},
		}, IDGateConfig{
			FailClosed:     false,
			ScoreThreshold: 0.6,
			CacheTTL:       time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		img := base64.StdEncoding.EncodeToString([]byte("leader-deadline-img"))
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

		leaderCtx, cancelLeader := context.WithTimeout(context.Background(), 60*time.Millisecond)
		var wg sync.WaitGroup
		defer func() {
			cancelLeader()
			wg.Wait()
		}()

		leaderReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body).WithContext(leaderCtx)
		followerReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)

		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(httptest.NewRecorder(), leaderReq)
		}()

		// synctest.Wait waits until the leader is durably blocked in OCR
		// without advancing fake time, so leaderCtx cannot expire prematurely.
		synctest.Wait()
		select {
		case <-leaderStarted:
		default:
			t.Fatal("leader did not enter OCR")
		}

		followerRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(followerRec, followerReq)
		}()

		synctest.Wait()
		select {
		case <-followerJoined:
		default:
			t.Fatal("follower did not join in-flight scan before leader expired")
		}

		// Advancing past 60ms expires leaderCtx with context.DeadlineExceeded
		// after the follower has joined; the live follower must retry and block.
		time.Sleep(60 * time.Millisecond)
		wg.Wait()

		if followerRec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("follower status = %d, want 422 (must retry and block after leader deadline expires)", followerRec.Code)
		}
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}
	})
}

func TestIDGateMiddleware_ScanBudgetAccountsForGlobalMaxInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ocrClient := &funcOCR{
			fn: func(ctx context.Context, _ []byte, _ string) (string, error) {
				timer := time.NewTimer(70 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
					return "ok", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		}

		// ImageConcurrency=4 > MaxInFlight=1 serializes 3 images (wait times
		// 0ms, 70ms, 140ms). A single-batch 110ms queue budget would expire
		// image 3 while queued; using budgetConcurrency = min(4, 1) = 1 scales
		// queueBudget to 3 batches (330ms), allowing all 3 scans to complete.
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:       true,
			ImageConcurrency: 4,
			MaxInFlight:      1,
			ScanTimeout:      110 * time.Millisecond,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		img1 := base64.StdEncoding.EncodeToString([]byte("serial-img-1"))
		img2 := base64.StdEncoding.EncodeToString([]byte("serial-img-2"))
		img3 := base64.StdEncoding.EncodeToString([]byte("serial-img-3"))
		body := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			img1, img2, img3,
		)

		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (scan budget must account for MaxInFlight=1)", rec.Code)
		}
	})
}

func TestIDGateMiddleware_AdmissionWaitDoesNotConsumeStageTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		firstEntered := make(chan struct{})
		ocrClient := &funcOCR{
			fn: func(ctx context.Context, img []byte, _ string) (string, error) {
				dur := 9 * time.Second
				if string(img) == "holder-img" {
					dur = 10 * time.Second
					close(firstEntered)
				}
				timer := time.NewTimer(dur)
				defer timer.Stop()
				select {
				case <-timer.C:
					return "clean", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		}

		// With QueueTimeout=12s, ScanTimeout=18s, and MaxInFlight=1: request 1
		// holds the sole slot for 10s, then request 2 is admitted and needs 9s
		// of OCR (19s total). Because stage timeout starts after global
		// admission, request 2 retains its full 18s stage budget and succeeds.
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:   true,
			MaxInFlight:  1,
			QueueTimeout: 12 * time.Second,
			ScanTimeout:  18 * time.Second,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		body1 := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString([]byte("holder-img")) + `"}}]}]}`
		body2 := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + base64.StdEncoding.EncodeToString([]byte("waiter-img")) + `"}}]}]}`

		rec1 := httptest.NewRecorder()
		rec2 := httptest.NewRecorder()
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(rec1, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body1))
		}()

		synctest.Wait()
		select {
		case <-firstEntered:
		default:
			t.Fatal("first request did not acquire global slot")
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body2))
		}()

		wg.Wait()
		if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
			t.Fatalf("statuses = (%d, %d), want (200, 200)", rec1.Code, rec2.Code)
		}
	})
}

func TestIDGateMiddleware_CoalescedBlockedFollowerLogsCacheHit(t *testing.T) {
	prevHook := idGateTestOnInflightWait
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	synctest.Test(t, func(t *testing.T) {
		leaderStarted := make(chan struct{})
		releaseLeader := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseLeader) }) }
		followerJoined := make(chan struct{}, 1)
		idGateTestOnInflightWait = func() { followerJoined <- struct{}{} }

		var wg sync.WaitGroup
		defer func() {
			release()
			wg.Wait()
		}()

		var logBuf bytes.Buffer
		var logMu sync.Mutex
		logger := slog.New(slog.NewJSONHandler(&lockedWriter{mu: &logMu, w: &logBuf}, nil))

		ocrClient := &funcOCR{
			fn: func(_ context.Context, _ []byte, _ string) (string, error) {
				close(leaderStarted)
				<-releaseLeader
				return "PASSPORT 123456789", nil
			},
		}
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{
			spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}},
		}, IDGateConfig{
			ScoreThreshold: 0.6,
			CacheTTL:       0,
			Logger:         logger,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		img := base64.StdEncoding.EncodeToString([]byte("coalesced-block-img"))
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

		leaderRec := httptest.NewRecorder()
		followerRec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(leaderRec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		}()

		synctest.Wait()
		select {
		case <-leaderStarted:
		default:
			t.Fatal("leader did not start OCR")
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(followerRec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		}()

		synctest.Wait()
		select {
		case <-followerJoined:
		default:
			t.Fatal("follower did not join in-flight scan")
		}

		release()
		wg.Wait()

		if leaderRec.Code != http.StatusUnprocessableEntity || followerRec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("statuses = (%d, %d), want (422, 422)", leaderRec.Code, followerRec.Code)
		}

		lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
		var blockLogs, cacheHitLogs int
		for _, line := range lines {
			if strings.Contains(line, "id_gate: blocked government identity document") {
				blockLogs++
				if strings.Contains(line, `"cache_hit":true`) {
					cacheHitLogs++
				}
			}
		}
		if blockLogs != 2 || cacheHitLogs != 1 {
			t.Fatalf("blockLogs=%d cacheHitLogs=%d, want 2 and 1; logs:\n%s", blockLogs, cacheHitLogs, logBuf.String())
		}
	})
}

type coalesceTestHarness struct {
	leaderStarted  chan struct{}
	releaseLeader  chan struct{}
	releaseOnce    sync.Once
	followerJoined chan struct{}
	wg             sync.WaitGroup
}

func newCoalesceTestHarness(buf int) *coalesceTestHarness {
	h := &coalesceTestHarness{
		leaderStarted:  make(chan struct{}),
		releaseLeader:  make(chan struct{}),
		followerJoined: make(chan struct{}, buf),
	}
	idGateTestOnInflightWait = func() { h.followerJoined <- struct{}{} }
	return h
}

func (h *coalesceTestHarness) release() {
	h.releaseOnce.Do(func() { close(h.releaseLeader) })
}

func (h *coalesceTestHarness) cleanup() {
	h.release()
	h.wg.Wait()
}

func (h *coalesceTestHarness) run(fn func()) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		fn()
	}()
}

func TestIDGateMiddleware_FollowerDoesNotHoldReqSemSlot(t *testing.T) {
	prevWaitHook := idGateTestOnInflightWait
	prevBeforeHook := idGateTestBeforeScan
	t.Cleanup(func() {
		idGateTestOnInflightWait = prevWaitHook
		idGateTestBeforeScan = prevBeforeHook
	})

	synctest.Test(t, func(t *testing.T) {
		h := newCoalesceTestHarness(1)
		allowDistinct := make(chan struct{})
		var allowOnce sync.Once
		unblockDistinct := func() { allowOnce.Do(func() { close(allowDistinct) }) }
		distinctScanned := make(chan struct{})
		defer func() {
			unblockDistinct()
			h.cleanup()
		}()

		// Hold request B's distinct-img (index 1) before admission until
		// shared-img (index 0) has deterministically joined the in-flight scan,
		// so the old reqSem-before-coalesce order would reliably deadlock here.
		idGateTestBeforeScan = func(imageIndex int) {
			if imageIndex == 1 {
				<-allowDistinct
			}
		}

		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				if string(img) == "shared-img" {
					close(h.leaderStarted)
					<-h.releaseLeader
					return "clean", nil
				}
				close(distinctScanned)
				return "clean", nil
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:       true,
			ImageConcurrency: 1,
			ScanTimeout:      time.Second,
			CacheTTL:         time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		sharedB64 := base64.StdEncoding.EncodeToString([]byte("shared-img"))
		distinctB64 := base64.StdEncoding.EncodeToString([]byte("distinct-img"))
		bodyA := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + sharedB64 + `"}}]}]}`
		bodyB := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			sharedB64, distinctB64,
		)

		recA := httptest.NewRecorder()
		recB := httptest.NewRecorder()
		h.run(func() { mw.ServeHTTP(recA, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyA)) })

		synctest.Wait()
		select {
		case <-h.leaderStarted:
		default:
			t.Fatal("request A did not start scanning shared-img")
		}

		h.run(func() { mw.ServeHTTP(recB, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyB)) })

		synctest.Wait()
		select {
		case <-h.followerJoined:
		default:
			t.Fatal("request B shared-img did not join in-flight scan")
		}

		// Now release distinct-img while shared-img is still coalesced and
		// request A has NOT been released; distinct-img must reach OCR now.
		unblockDistinct()
		synctest.Wait()
		select {
		case <-distinctScanned:
		default:
			t.Fatal("request B distinct-img was blocked by coalesced follower holding reqSem")
		}

		h.release()
		h.wg.Wait()
		if recA.Code != http.StatusOK || recB.Code != http.StatusOK {
			t.Fatalf("statuses = (%d, %d), want (200, 200)", recA.Code, recB.Code)
		}
	})
}

func TestIDGateMiddleware_QueuedLeaderDoesNotBlockUnblockedRequest(t *testing.T) {
	prevBeforeHook := idGateTestBeforeScan
	t.Cleanup(func() { idGateTestBeforeScan = prevBeforeHook })

	synctest.Test(t, func(t *testing.T) {
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
		allowQueuedX := make(chan struct{})
		var allowOnce sync.Once
		unblockQueuedX := func() { allowOnce.Do(func() { close(allowQueuedX) }) }
		xScanned := make(chan struct{})

		var wg sync.WaitGroup
		defer func() {
			unblockQueuedX()
			release()
			wg.Wait()
		}()

		// Ensure request A's first image (index 0) acquires reqSem before A's
		// second image X (index 1) attempts admission and queues on reqSem.
		idGateTestBeforeScan = func(imageIndex int) {
			if imageIndex == 1 {
				<-allowQueuedX
			}
		}

		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				if string(img) == "slow-first-img" {
					close(firstStarted)
					<-releaseFirst
					return "clean", nil
				}
				close(xScanned)
				return "clean", nil
			},
		}

		// ImageConcurrency=1, MaxInFlight=2: A has [slow-first-img, x-img] so
		// A's x-img is queued on A's reqSem while a global slot is free.
		// Request B arrives with only x-img and must scan x-img immediately
		// rather than blocking behind A's unadmitted queued worker.
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:       true,
			ImageConcurrency: 1,
			MaxInFlight:      2,
			ScanTimeout:      time.Second,
			CacheTTL:         time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		firstB64 := base64.StdEncoding.EncodeToString([]byte("slow-first-img"))
		xB64 := base64.StdEncoding.EncodeToString([]byte("x-img"))
		bodyA := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			firstB64, xB64,
		)
		bodyB := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + xB64 + `"}}]}]}`

		recA := httptest.NewRecorder()
		recB := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(recA, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyA))
		}()

		synctest.Wait()
		select {
		case <-firstStarted:
		default:
			t.Fatal("request A did not start scanning slow-first-img")
		}

		// Let A's x-img enter admission and block waiting on A's reqSem.
		unblockQueuedX()
		synctest.Wait()
		select {
		case <-xScanned:
			t.Fatal("request A bypassed reqSem and scanned x-img while slow-first-img held the sole slot")
		default:
		}

		// Request B arrives with x-img and a free reqSem + globalSem slot; it
		// must scan x-img and finish while A's slow-first-img is still held.
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(recB, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyB))
		}()

		synctest.Wait()
		select {
		case <-xScanned:
		default:
			t.Fatal("request B was blocked by request A's unadmitted queued worker")
		}
		if recB.Code != http.StatusOK {
			t.Fatalf("request B status = %d, want 200 before request A finishes", recB.Code)
		}

		release()
		wg.Wait()
		if recA.Code != http.StatusOK {
			t.Fatalf("request A status = %d, want 200", recA.Code)
		}
	})
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}

type recordingMetricsSink struct {
	mu            sync.Mutex
	incrs         []recordedMetric
	distributions []recordedMetric
}

type recordedMetric struct {
	name  string
	value float64
	tags  []string
}

func (m *recordingMetricsSink) Incr(name string, tags []string, _ float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.incrs = append(m.incrs, recordedMetric{name: name, value: 1, tags: append([]string(nil), tags...)})
	return nil
}

func (m *recordingMetricsSink) Distribution(name string, value float64, tags []string, _ float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.distributions = append(m.distributions, recordedMetric{name: name, value: value, tags: append([]string(nil), tags...)})
	return nil
}

func (m *recordingMetricsSink) countIncr(name string, requiredTags ...string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, rec := range m.incrs {
		if rec.name != name {
			continue
		}
		match := true
		for _, want := range requiredTags {
			if !slices.Contains(rec.tags, want) {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

func (m *recordingMetricsSink) countDistribution(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, rec := range m.distributions {
		if rec.name == name {
			n++
		}
	}
	return n
}

func TestIDGateMiddleware_MaxInFlightBoundsCrossRequestConcurrency(t *testing.T) {
	const capLimit = 3
	const requests = 10

	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	reachedCap := make(chan struct{})
	var capOnce sync.Once
	releaseFirstWave := make(chan struct{})

	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			cur := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur == capLimit {
				capOnce.Do(func() { close(reachedCap) })
			}
			<-releaseFirstWave
			inFlight.Add(-1)
			return "ok", nil
		},
	}

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		MaxInFlight:  capLimit,
		QueueTimeout: 5 * time.Second,
		ScanTimeout:  5 * time.Second,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			img := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("distinct-img-%d", idx)))
			body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
			if rec.Code != http.StatusOK {
				t.Errorf("req %d status = %d, want 200", idx, rec.Code)
			}
		}(i)
	}

	select {
	case <-reachedCap:
	case <-time.After(5 * time.Second):
		close(releaseFirstWave)
		wg.Wait()
		t.Fatalf("timed out waiting for %d concurrent OCR calls", capLimit)
	}
	close(releaseFirstWave)
	wg.Wait()

	if maxSeen.Load() != capLimit {
		t.Fatalf("max concurrent OCR calls = %d, want exactly %d", maxSeen.Load(), capLimit)
	}
}

func TestIDGateMiddleware_QueueWaitTimesOutBeforeCallingOCR(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var ocrCalls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			if ocrCalls.Add(1) == 1 {
				close(firstEntered)
				<-releaseFirst
			}
			return "ok", nil
		},
	}

	metrics := &recordingMetricsSink{}
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		FailClosed:   true,
		MaxInFlight:  1,
		QueueTimeout: 40 * time.Millisecond,
		ScanTimeout:  5 * time.Second,
		Metrics:      metrics,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	img1 := base64.StdEncoding.EncodeToString([]byte("slow-img-1"))
	body1 := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img1 + `"}}]}]}`
	img2 := base64.StdEncoding.EncodeToString([]byte("queued-img-2"))
	body2 := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img2 + `"}}]}]}`

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body1))
	}()

	<-firstEntered
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body2))

	close(releaseFirst)
	wg.Wait()

	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("queued request status = %d, want 503", rec2.Code)
	}
	if ocrCalls.Load() != 1 {
		t.Fatalf("ocr calls = %d, want 1 (timed-out queued request must not hit OCR)", ocrCalls.Load())
	}
	if got := metrics.countIncr("id_gate.scan_failed", "stage:queue", "outcome:fail_closed"); got != 1 {
		t.Fatalf("id_gate.scan_failed stage:queue count = %d, want 1", got)
	}
	if got := metrics.countDistribution("id_gate.queue_wait_ms"); got == 0 {
		t.Fatal("expected id_gate.queue_wait_ms metric to be emitted")
	}
}

func TestIDGateMiddleware_CacheDisabledAtZeroTTL(t *testing.T) {
	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			calls.Add(1)
			return "plain text", nil
		},
	}

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		CacheTTL: 0,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	img := base64.StdEncoding.EncodeToString([]byte("no-cache-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	for i := range 2 {
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("req %d status = %d, want 200", i, rec.Code)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("ocr calls = %d, want 2 when CacheTTL == 0", calls.Load())
	}
}

func TestIDGateMiddleware_CacheExpiryRescansAfterTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		ocrClient := &funcOCR{
			fn: func(_ context.Context, _ []byte, _ string) (string, error) {
				calls.Add(1)
				return "plain text", nil
			},
		}

		metrics := &recordingMetricsSink{}
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			CacheTTL: 50 * time.Millisecond,
			Metrics:  metrics,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		img := base64.StdEncoding.EncodeToString([]byte("expiring-img"))
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

		mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		if calls.Load() != 1 {
			t.Fatalf("before expiry ocr calls = %d, want 1", calls.Load())
		}
		if got := metrics.countIncr("id_gate.cache_hit"); got != 1 {
			t.Fatalf("id_gate.cache_hit count = %d, want 1", got)
		}

		time.Sleep(60 * time.Millisecond)

		mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		if calls.Load() != 2 {
			t.Fatalf("after expiry ocr calls = %d, want 2", calls.Load())
		}
	})
}

func TestIDGateMiddleware_CacheEvictionBoundsMaxEntries(t *testing.T) {
	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			calls.Add(1)
			return "plain text", nil
		},
	}

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		CacheTTL:        time.Minute,
		CacheMaxEntries: 2,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	makeBody := func(label string) string {
		img := base64.StdEncoding.EncodeToString([]byte(label))
		return `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`
	}

	// Fill cache to capacity (2) and insert a 3rd distinct image to force eviction.
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-1")))
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-2")))
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-3")))
	if calls.Load() != 3 {
		t.Fatalf("initial calls = %d, want 3", calls.Load())
	}

	// Re-checking all 3 must trigger at least 1 miss because maxEntries=2
	// evicted one of the first two entries.
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-1")))
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-2")))
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", makeBody("img-3")))
	if calls.Load() < 4 {
		t.Fatalf("calls after eviction check = %d, want >= 4", calls.Load())
	}
}

func TestIDGateMiddleware_CacheRecheckOnAdmissionTimeout(t *testing.T) {
	prevBefore := idGateTestBeforeScan
	t.Cleanup(func() { idGateTestBeforeScan = prevBefore })

	synctest.Test(t, func(t *testing.T) {
		slowStarted := make(chan struct{})
		releaseSlow := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseSlow) }) }
		allowQueuedTarget := make(chan struct{})
		var allowOnce sync.Once
		unblockTarget := func() { allowOnce.Do(func() { close(allowQueuedTarget) }) }

		var wg sync.WaitGroup
		defer func() {
			unblockTarget()
			release()
			wg.Wait()
		}()

		idGateTestBeforeScan = func(imageIndex int) {
			if imageIndex == 1 {
				<-allowQueuedTarget
			}
		}

		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				if string(img) == "slow-blocker" {
					close(slowStarted)
					<-releaseSlow
					return "clean", nil
				}
				return "PASSPORT 123456789", nil
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{
			spans: []redact.Span{{EntityType: "US_PASSPORT", Score: 0.9}},
		}, IDGateConfig{
			FailClosed:       true,
			ScoreThreshold:   0.6,
			ImageConcurrency: 1,
			MaxInFlight:      2,
			QueueTimeout:     50 * time.Millisecond,
			ScanTimeout:      time.Second,
			CacheTTL:         time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		slowB64 := base64.StdEncoding.EncodeToString([]byte("slow-blocker"))
		targetB64 := base64.StdEncoding.EncodeToString([]byte("target-passport"))
		bodyA := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			slowB64, targetB64,
		)
		bodyB := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + targetB64 + `"}}]}]}`

		recA := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(recA, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyA))
		}()

		synctest.Wait()
		<-slowStarted

		// Let A's target-passport worker block waiting on A's reqSem.
		unblockTarget()
		synctest.Wait()

		// While A's target-passport worker is waiting on reqSem, request B
		// scans target-passport and caches the blocked verdict.
		recB := httptest.NewRecorder()
		mw.ServeHTTP(recB, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyB))
		if recB.Code != http.StatusUnprocessableEntity {
			t.Fatalf("req B status = %d, want 422", recB.Code)
		}

		// Expire A's queueCtx (2 batches * 50ms = 100ms). On admission timeout,
		// scanCache.do must re-check the cache and return the cached 422 block
		// instead of 503.
		time.Sleep(110 * time.Millisecond)
		release()
		wg.Wait()

		if recA.Code != http.StatusUnprocessableEntity {
			t.Fatalf("req A status = %d, want 422 from cache re-check on admission timeout", recA.Code)
		}
	})
}

func TestIDGateMiddleware_PublishRaceReleasesSlot(t *testing.T) {
	prevAfterAcquire := idGateTestAfterAcquire
	t.Cleanup(func() { idGateTestAfterAcquire = prevAfterAcquire })

	synctest.Test(t, func(t *testing.T) {
		var acquireCount atomic.Int32
		worker2Acquired := make(chan struct{})
		resumeWorker2 := make(chan struct{})
		var resumeOnce sync.Once
		resume2 := func() { resumeOnce.Do(func() { close(resumeWorker2) }) }
		leaderInOCR := make(chan struct{})
		releaseLeader := make(chan struct{})
		var releaseOnce sync.Once
		release1 := func() { releaseOnce.Do(func() { close(releaseLeader) }) }
		allowOther := make(chan struct{})
		var allowOnce sync.Once
		unblockOther := func() { allowOnce.Do(func() { close(allowOther) }) }

		var wg sync.WaitGroup
		defer func() {
			unblockOther()
			resume2()
			release1()
			wg.Wait()
		}()

		// First caller to acquire (req2) pauses right after acquire() before
		// publishing inflight[digest]; second caller (req1) acquires and
		// publishes inflight[digest] first. When req2 resumes it must release
		// its acquired reqSem slot and coalesce onto req1 without leaking a
		// slot or triggering a second OCR call.
		idGateTestAfterAcquire = func() {
			if acquireCount.Add(1) == 1 {
				close(worker2Acquired)
				<-resumeWorker2
			}
		}

		otherScanned := make(chan struct{})
		var ocrCalls atomic.Int32
		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				if string(img) == "race-img" {
					if ocrCalls.Add(1) == 1 {
						close(leaderInOCR)
						<-releaseLeader
					}
					return "clean", nil
				}
				close(otherScanned)
				return "clean", nil
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:       true,
			ImageConcurrency: 1,
			ScanTimeout:      time.Second,
			CacheTTL:         time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		raceB64 := base64.StdEncoding.EncodeToString([]byte("race-img"))
		otherB64 := base64.StdEncoding.EncodeToString([]byte("other-img"))
		body1 := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + raceB64 + `"}}]}]}`
		body2 := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			raceB64, otherB64,
		)

		prevBefore := idGateTestBeforeScan
		idGateTestBeforeScan = func(imageIndex int) {
			if imageIndex == 1 {
				<-allowOther
			}
		}
		defer func() { idGateTestBeforeScan = prevBefore }()

		rec1 := httptest.NewRecorder()
		rec2 := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body2))
		}()

		synctest.Wait()
		<-worker2Acquired

		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(rec1, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body1))
		}()

		synctest.Wait()
		<-leaderInOCR

		// Resume req2 so it sees req1's inflight entry, releases its reqSem
		// slot, and lets other-img acquire reqSem while req1 is still in OCR.
		resume2()
		unblockOther()
		synctest.Wait()
		select {
		case <-otherScanned:
		default:
			t.Fatal("other-img was blocked — req2 leaked its reqSem slot on publish race")
		}

		release1()
		wg.Wait()

		if rec1.Code != http.StatusOK || rec2.Code != http.StatusOK {
			t.Fatalf("statuses = (%d, %d), want (200, 200)", rec1.Code, rec2.Code)
		}
		if ocrCalls.Load() != 1 {
			t.Fatalf("race-img ocr calls = %d, want 1", ocrCalls.Load())
		}
	})
}

func TestIDGateMiddleware_PerRequestAdmissionTimeoutReportsQueueStage(t *testing.T) {
	prevBefore := idGateTestBeforeScan
	t.Cleanup(func() { idGateTestBeforeScan = prevBefore })

	synctest.Test(t, func(t *testing.T) {
		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var releaseOnce sync.Once
		release1 := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
		allowSecond := make(chan struct{})
		var allowOnce sync.Once
		unblock2 := func() { allowOnce.Do(func() { close(allowSecond) }) }
		var wg sync.WaitGroup
		defer func() {
			unblock2()
			release1()
			wg.Wait()
		}()

		idGateTestBeforeScan = func(imageIndex int) {
			if imageIndex == 1 {
				<-allowSecond
			}
		}

		ocrClient := &funcOCR{
			fn: func(ctx context.Context, img []byte, _ string) (string, error) {
				if string(img) == "img-a" {
					close(firstStarted)
					select {
					case <-releaseFirst:
						return "clean", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}
				return "clean", nil
			},
		}

		metrics := &recordingMetricsSink{}
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:       true,
			ImageConcurrency: 1,
			QueueTimeout:     40 * time.Millisecond,
			ScanTimeout:      5 * time.Second,
			Metrics:          metrics,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		aB64 := base64.StdEncoding.EncodeToString([]byte("img-a"))
		bB64 := base64.StdEncoding.EncodeToString([]byte("img-b"))
		body := fmt.Sprintf(
			`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,%s"}}]}]}`,
			aB64, bB64,
		)

		rec := httptest.NewRecorder()
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
		}()

		synctest.Wait()
		<-firstStarted
		unblock2()
		synctest.Wait()

		// 2 images at concurrency 1 -> queueBudget = 2 * 40ms = 80ms.
		// Advancing past 80ms expires reqSem wait for img-b while img-a still
		// has 5s ScanTimeout.
		time.Sleep(90 * time.Millisecond)
		release1()
		wg.Wait()

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 on per-request admission timeout", rec.Code)
		}
		if got := metrics.countIncr("id_gate.scan_failed", "stage:queue", "outcome:fail_closed"); got != 1 {
			t.Fatalf("id_gate.scan_failed stage:queue count = %d, want 1", got)
		}
	})
}

func TestIDGateMiddleware_SharedUpstreamTimeoutDoesNotRetryFollowers(t *testing.T) {
	prevHook := idGateTestOnInflightWait
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	synctest.Test(t, func(t *testing.T) {
		h := newCoalesceTestHarness(1)
		defer h.cleanup()

		var calls atomic.Int32
		ocrClient := &funcOCR{
			fn: func(_ context.Context, _ []byte, _ string) (string, error) {
				if calls.Add(1) == 1 {
					close(h.leaderStarted)
					<-h.releaseLeader
				}
				return "", fmt.Errorf("ocr: extract-text call failed: %w", context.DeadlineExceeded)
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed: true,
			CacheTTL:   time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		img := base64.StdEncoding.EncodeToString([]byte("upstream-timeout-img"))
		body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

		leaderRec := httptest.NewRecorder()
		followerRec := httptest.NewRecorder()
		h.run(func() { mw.ServeHTTP(leaderRec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)) })
		synctest.Wait()
		<-h.leaderStarted

		h.run(func() { mw.ServeHTTP(followerRec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)) })
		synctest.Wait()
		<-h.followerJoined

		h.release()
		h.wg.Wait()

		if leaderRec.Code != http.StatusServiceUnavailable || followerRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("statuses = (%d, %d), want (503, 503)", leaderRec.Code, followerRec.Code)
		}
		if calls.Load() != 1 {
			t.Fatalf("ocr calls = %d, want 1 (shared upstream timeout must not re-hammer OCR)", calls.Load())
		}
	})
}

func TestComputeIDGateQueueTimeout(t *testing.T) {
	if got := computeIDGateQueueTimeout(1, 4, 5*time.Second); got != 5*time.Second {
		t.Fatalf("1 image = %v, want 5s", got)
	}
	if got := computeIDGateQueueTimeout(4, 4, 5*time.Second); got != 5*time.Second {
		t.Fatalf("4 images (1 batch) = %v, want 5s", got)
	}
	if got := computeIDGateQueueTimeout(5, 4, 5*time.Second); got != 10*time.Second {
		t.Fatalf("5 images (2 batches) = %v, want 10s", got)
	}
	if got := computeIDGateQueueTimeout(40, 4, 5*time.Second); got != 15*time.Second {
		t.Fatalf("40 images = %v, want 15s cap", got)
	}
}
