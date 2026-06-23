package middleware

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if rec.Body.String() != idGateBlockMessage+"\n" {
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
