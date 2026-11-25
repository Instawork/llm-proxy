package ocr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
		if r.Header.Get("X-OCR-Token") != "test-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "PASSPORT 123456789"})
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token", 2*time.Second)
	text, err := client.ExtractText(context.Background(), []byte("fake-image"), "image-0.bin")
	if err != nil {
		t.Fatalf("ExtractText: %v", err)
	}
	if text != "PASSPORT 123456789" {
		t.Fatalf("text = %q", text)
	}
}

func TestClientExtractTextErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token", time.Second)
	_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("503 should not wrap ErrUnauthorized: %v", err)
	}
}

func TestClientExtractTextAuthErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad token", status)
			}))
			defer srv.Close()

			client := New(srv.URL, "wrong-token", time.Second)
			_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
			if !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("status %d: err = %v, want ErrUnauthorized", status, err)
			}
		})
	}
}

func TestClientExtractTextInvalidTokenFailsUnauthorized(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "OK"})
	}))
	defer srv.Close()

	for _, badToken := range []string{"", "tok\nen", "tok\ren", "tok\x00en", "tok\x7fen"} {
		client := New(srv.URL, badToken, time.Second)
		_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("token %q: err = %v, want ErrUnauthorized", badToken, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server called %d times for invalid tokens; expected 0", hits.Load())
	}
}

func TestClientExtractTextDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "LEAKED"})
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/extract-text", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	client := New(redirector.URL, "secret-token", time.Second)
	_, err := client.ExtractText(context.Background(), []byte("sensitive-image"), "id.png")
	if err == nil {
		t.Fatal("expected error on 307 redirect")
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was called %d times; token/image must not follow redirects", targetHits.Load())
	}
}
