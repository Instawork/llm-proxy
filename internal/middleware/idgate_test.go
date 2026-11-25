package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	// CacheTTL < 0 disables completed-result caching so only true in-flight
	// coalescing can keep OCR calls at 1.
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{CacheTTL: -1})(
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

		// With ScanTimeout=18s and MaxInFlight=1: request 1 holds the sole slot
		// for 10s, then request 2 is admitted and needs 9s of OCR (19s total).
		// Because stage timeout starts after global admission, request 2 retains
		// its full 18s stage budget and succeeds.
		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			FailClosed:  true,
			MaxInFlight: 1,
			ScanTimeout: 18 * time.Second,
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
			CacheTTL:       -1,
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

func TestIDGateMiddleware_DoesNotCacheDecodeAdmissionRefusedVerdict(t *testing.T) {
	largePNG := makeSyntheticPNG(t, 600, 400)
	var calls atomic.Int32
	var seenBodies [][]byte
	ocrClient := &funcOCR{
		fn: func(_ context.Context, img []byte, _ string) (string, error) {
			calls.Add(1)
			seenBodies = append(seenBodies, append([]byte(nil), img...))
			return "plain text", nil
		},
	}

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		MaxBodyBytes:      10 * 1024 * 1024,
		MaxImageDimension: 400,
		CacheTTL:          time.Minute,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	imgB64 := base64.StdEncoding.EncodeToString(largePNG)
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imgB64 + `"}}]}]}`

	// Exhaust the shared decode budget so the first scan falls back to raw bytes.
	if !ocrDecodeBudget.tryAcquire(maxAggregateDecodeBytes) {
		t.Fatal("expected to reserve full decode budget")
	}
	released := false
	t.Cleanup(func() {
		if !released {
			ocrDecodeBudget.release(maxAggregateDecodeBytes)
		}
	})

	rec1 := httptest.NewRecorder()
	mw.ServeHTTP(rec1, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	if rec1.Code != http.StatusOK {
		t.Fatalf("req1 status = %d, want 200", rec1.Code)
	}
	if len(seenBodies) != 1 || !bytes.Equal(seenBodies[0], largePNG) {
		t.Fatalf("req1 must pass exact original PNG bytes when decode admission is refused")
	}

	// Release decode capacity. Because the first verdict was produced under
	// decode admission refusal, it must NOT be cached; req2 must re-scan with
	// the downscaled 400x267 JPEG and cache that result for req3.
	ocrDecodeBudget.release(maxAggregateDecodeBytes)
	released = true

	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	if rec2.Code != http.StatusOK {
		t.Fatalf("req2 status = %d, want 200", rec2.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after req2 = %d, want 2 (admission-refused verdict must not be cached)", calls.Load())
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(seenBodies[1]))
	if err != nil {
		t.Fatalf("decode req2 OCR input: %v", err)
	}
	if format != "jpeg" || cfg.Width != 400 || cfg.Height != 267 {
		t.Fatalf("req2 OCR input = (%q, %dx%d), want (jpeg, 400x267)", format, cfg.Width, cfg.Height)
	}

	rec3 := httptest.NewRecorder()
	mw.ServeHTTP(rec3, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
	if rec3.Code != http.StatusOK {
		t.Fatalf("req3 status = %d, want 200", rec3.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls after req3 = %d, want 2 (downscaled verdict should be cached)", calls.Load())
	}
}

func TestIDGateMiddleware_InflightFollowerDoesNotReuseAdmissionRefusedVerdict(t *testing.T) {
	prevHook := idGateTestOnInflightWait
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	largePNG := makeSyntheticPNG(t, 600, 400)
	imgB64 := base64.StdEncoding.EncodeToString(largePNG)
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imgB64 + `"}}]}]}`

	if !ocrDecodeBudget.tryAcquire(maxAggregateDecodeBytes) {
		t.Fatal("expected to reserve full decode budget")
	}
	budgetReleased := false
	releaseBudget := func() {
		if !budgetReleased {
			ocrDecodeBudget.release(maxAggregateDecodeBytes)
			budgetReleased = true
		}
	}
	t.Cleanup(releaseBudget)

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

		var mu sync.Mutex
		var seenBodies [][]byte
		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				mu.Lock()
				n := len(seenBodies) + 1
				seenBodies = append(seenBodies, append([]byte(nil), img...))
				mu.Unlock()
				if n == 1 {
					close(leaderStarted)
					<-releaseLeader
				}
				return "plain text", nil
			},
		}

		mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
			MaxBodyBytes:      10 * 1024 * 1024,
			MaxImageDimension: 400,
			CacheTTL:          time.Minute,
		})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

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

		// Capacity recovers before the leader finishes; follower must run its
		// own admission/downscale instead of reusing the non-cacheable verdict.
		releaseBudget()
		release()
		wg.Wait()

		if leaderRec.Code != http.StatusOK || followerRec.Code != http.StatusOK {
			t.Fatalf("statuses = (%d, %d), want (200, 200)", leaderRec.Code, followerRec.Code)
		}
		if len(seenBodies) != 2 {
			t.Fatalf("ocr calls = %d, want 2 (follower must not reuse admission-refused verdict)", len(seenBodies))
		}
		if !bytes.Equal(seenBodies[0], largePNG) {
			t.Fatal("leader should have scanned raw PNG under budget refusal")
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(seenBodies[1]))
		if err != nil {
			t.Fatalf("decode follower OCR input: %v", err)
		}
		if format != "jpeg" || cfg.Width != 400 || cfg.Height != 267 {
			t.Fatalf("follower OCR input = (%q, %dx%d), want (jpeg, 400x267)", format, cfg.Width, cfg.Height)
		}
	})
}

func TestIDGateMiddleware_FollowerDoesNotHoldReqSemSlot(t *testing.T) {
	prevHook := idGateTestOnInflightWait
	t.Cleanup(func() { idGateTestOnInflightWait = prevHook })

	synctest.Test(t, func(t *testing.T) {
		sharedStarted := make(chan struct{})
		releaseShared := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseShared) }) }
		followerJoined := make(chan struct{}, 1)
		idGateTestOnInflightWait = func() { followerJoined <- struct{}{} }
		distinctScanned := make(chan struct{})

		var wg sync.WaitGroup
		defer func() {
			release()
			wg.Wait()
		}()

		ocrClient := &funcOCR{
			fn: func(_ context.Context, img []byte, _ string) (string, error) {
				if string(img) == "shared-img" {
					close(sharedStarted)
					<-releaseShared
					return "clean", nil
				}
				close(distinctScanned)
				return "clean", nil
			},
		}

		// ImageConcurrency=1: request B has [shared-img, distinct-img]. While
		// shared-img is coalesced behind request A, distinct-img must NOT be
		// blocked from acquiring reqSem and scanning immediately.
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(recA, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyA))
		}()

		synctest.Wait()
		select {
		case <-sharedStarted:
		default:
			t.Fatal("request A did not start scanning shared-img")
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			mw.ServeHTTP(recB, newReq(t, http.MethodPost, "/openai/v1/chat/completions", bodyB))
		}()

		synctest.Wait()
		select {
		case <-followerJoined:
		default:
			t.Fatal("request B shared-img did not join in-flight scan")
		}
		select {
		case <-distinctScanned:
		default:
			t.Fatal("request B distinct-img was blocked by coalesced follower holding reqSem")
		}

		release()
		wg.Wait()
		if recA.Code != http.StatusOK || recB.Code != http.StatusOK {
			t.Fatalf("statuses = (%d, %d), want (200, 200)", recA.Code, recB.Code)
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

func TestIDGateMiddleware_MaxInFlightBoundsCrossRequestConcurrency(t *testing.T) {
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			cur := inFlight.Add(1)
			for {
				prev := maxSeen.Load()
				if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			inFlight.Add(-1)
			return "ok", nil
		},
	}

	const capLimit = 3
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		MaxInFlight: capLimit,
		ScanTimeout: 5 * time.Second,
	})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const requests = 10
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
	wg.Wait()

	if maxSeen.Load() > capLimit {
		t.Fatalf("max concurrent OCR calls = %d, want <= %d", maxSeen.Load(), capLimit)
	}
	if maxSeen.Load() == 0 {
		t.Fatal("expected OCR calls to execute")
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

	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		FailClosed:  true,
		MaxInFlight: 1,
		ScanTimeout: 40 * time.Millisecond,
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
}

func TestIDGateMiddleware_DownscalesForOCRWithoutMutatingUpstreamBody(t *testing.T) {
	largePNG := makeSyntheticPNG(t, 600, 400)
	var seenOCRBytes []byte
	ocrClient := &funcOCR{
		fn: func(_ context.Context, img []byte, _ string) (string, error) {
			seenOCRBytes = append([]byte(nil), img...)
			return "clear text", nil
		},
	}

	imgB64 := base64.StdEncoding.EncodeToString(largePNG)
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + imgB64 + `"}}]}]}`

	cap := &captureHandler{}
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{
		MaxBodyBytes:      10 * 1024 * 1024,
		MaxImageDimension: 400,
	})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if cap.reqSeen == nil {
		t.Fatal("expected downstream handler")
	}
	if string(cap.bodySeen) != body {
		t.Fatal("upstream request body must not be mutated by OCR downscaling")
	}
	if len(seenOCRBytes) == 0 {
		t.Fatal("expected OCR to receive downscaled image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(seenOCRBytes))
	if err != nil {
		t.Fatalf("decode OCR image config: %v", err)
	}
	if cfg.Width != 400 || cfg.Height != 267 {
		t.Fatalf("OCR image dims = %dx%d, want 400x267", cfg.Width, cfg.Height)
	}
}

func TestComputeIDGateScanTimeout(t *testing.T) {
	if got := computeIDGateScanTimeout(1, 4, 15*time.Second); got != 15*time.Second {
		t.Fatalf("1 image = %v, want 15s", got)
	}
	if got := computeIDGateScanTimeout(4, 4, 15*time.Second); got != 15*time.Second {
		t.Fatalf("4 images (1 batch) = %v, want 15s", got)
	}
	if got := computeIDGateScanTimeout(5, 4, 15*time.Second); got != 30*time.Second {
		t.Fatalf("5 images (2 batches) = %v, want 30s", got)
	}
	if got := computeIDGateScanTimeout(40, 4, 15*time.Second); got != 60*time.Second {
		t.Fatalf("40 images = %v, want 60s cap", got)
	}
}
