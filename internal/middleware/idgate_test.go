package middleware

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
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
	var calls atomic.Int32
	ocrClient := &funcOCR{
		fn: func(_ context.Context, _ []byte, _ string) (string, error) {
			calls.Add(1)
			time.Sleep(40 * time.Millisecond)
			return "receipt text", nil
		},
	}
	mw := IDGateMiddleware(ocrClient, &fakeIDAnalyzer{}, IDGateConfig{CacheTTL: time.Minute})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	img := base64.StdEncoding.EncodeToString([]byte("shared-burst-img"))
	body := `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,` + img + `"}}]}]}`

	const n = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
		}()
	}
	close(start)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("concurrent ocr calls = %d, want 1", calls.Load())
	}
}

func TestIDGateMiddleware_CoalescedLeaderCancelDoesNotPoisonFollower(t *testing.T) {
	leaderStarted := make(chan struct{})
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

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body).WithContext(leaderCtx)
	followerReq := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mw.ServeHTTP(httptest.NewRecorder(), leaderReq)
	}()

	<-leaderStarted
	followerRec := httptest.NewRecorder()
	go func() {
		defer wg.Done()
		mw.ServeHTTP(followerRec, followerReq)
	}()

	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	wg.Wait()

	if followerRec.Code != http.StatusOK {
		t.Fatalf("follower status = %d, want 200 (must retry when leader cancels)", followerRec.Code)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
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
