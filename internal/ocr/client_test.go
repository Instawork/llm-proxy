package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestClientExtractText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/extract-text" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer file.Close()
		if header.Filename != "image-0.bin" {
			http.Error(w, "unexpected filename", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "PASSPORT 123456789"})
	}))
	defer srv.Close()

	client := New(srv.URL, 2*time.Second)
	text, err := client.ExtractText(context.Background(), []byte("fake-image"), "image-0.bin")
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if text != "PASSPORT 123456789" {
		t.Fatalf("text = %q", text)
	}
}

func TestClientExtractTextErrorStatus(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := New(srv.URL, time.Second)
	client.retryBaseDelay = 5 * time.Millisecond
	_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (503 busy must not retry and hold a concurrency slot)", attempts.Load())
	}
}

func TestClientExtractTextRejectsMissingOrNullText(t *testing.T) {
	for _, body := range []string{`{}`, `{"error":"degraded"}`, `{"text":null}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))

		client := New(srv.URL, time.Second)
		_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
		srv.Close()
		if err == nil {
			t.Fatalf("body %s: expected error for missing/null text field", body)
		}
	}
}

func TestClientTransportConfigured(t *testing.T) {
	client := New("http://ocr.local", 5*time.Second)
	tr, ok := client.client.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("expected dedicated *http.Transport, got %T", client.client.Transport)
	}
	if tr.MaxIdleConnsPerHost < 32 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want >= 32", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxIdleConns < 64 {
		t.Fatalf("MaxIdleConns = %d, want >= 64", tr.MaxIdleConns)
	}
}

func TestClientExtractTextRetriesTransient502OnceThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = file.Close()

		if n == 1 {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "RECOVERED"})
	}))
	defer srv.Close()

	client := New(srv.URL, 2*time.Second)
	client.retryBaseDelay = 5 * time.Millisecond

	text, err := client.ExtractText(context.Background(), []byte("fake-image"), "image-0.bin")
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if text != "RECOVERED" {
		t.Fatalf("text = %q, want RECOVERED", text)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestClientExtractTextDoesNotRetryNonTransientErrors(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusInternalServerError,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			http.Error(w, "fatal", status)
		}))

		client := New(srv.URL, 2*time.Second)
		client.retryBaseDelay = 5 * time.Millisecond

		_, err := client.ExtractText(context.Background(), []byte("fake-image"), "image-0.bin")
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		if attempts.Load() != 1 {
			t.Fatalf("status %d: attempts = %d, want 1 (no retry)", status, attempts.Load())
		}
	}
}

func TestClientExtractTextRespectsTimeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer func() {
		close(done)
		srv.Close()
	}()

	client := New(srv.URL, 60*time.Millisecond)
	start := time.Now()
	_, err := client.ExtractText(context.Background(), []byte("fake-image"), "image-0.bin")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("timeout took %v, expected prompt cancellation", elapsed)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type onCloseReadCloser struct {
	io.ReadCloser
	onClose func()
}

func (c *onCloseReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.onClose != nil {
		c.onClose()
	}
	return err
}

func TestClientExtractTextPreservesCancelCauseDuringRetryBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	client := New(srv.URL, 5*time.Second)
	client.retryBaseDelay = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	base := client.client.Transport
	client.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(req)
		if err == nil && resp != nil {
			// Trigger cancel right after attempt 1's response body is closed
			// so ExtractText enters retry backoff with a canceled context.
			resp.Body = &onCloseReadCloser{
				ReadCloser: resp.Body,
				onClose:    cancel,
			}
		}
		return resp, err
	})

	_, err := client.ExtractText(ctx, []byte("fake-image"), "image-0.bin")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got %v", err)
	}
	if !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 status detail in error, got %v", err)
	}
}

func TestClientExtractTextPreservesCancelCauseWhileBlockedInBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts atomic.Int32
		client := New("http://ocr.local", 5*time.Second)
		client.retryBaseDelay = 2 * time.Second
		client.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
				Header:     make(http.Header),
			}, nil
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			_, err := client.ExtractText(ctx, []byte("fake-image"), "image-0.bin")
			errCh <- err
		}()

		// Wait until ExtractText has finished attempt 1 and is durably blocked
		// inside sleepWithContext waiting on the 2s retry backoff timer.
		synctest.Wait()
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1 before backoff cancel", attempts.Load())
		}
		cancel()

		err := <-errCh
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled in error chain, got %v", err)
		}
		if !strings.Contains(err.Error(), "502") {
			t.Fatalf("expected 502 status detail in error, got %v", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("attempts = %d, want 1 (must not start attempt 2 after cancel)", attempts.Load())
		}
	})
}
