package ocr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := New(srv.URL, time.Second)
	_, err := client.ExtractText(context.Background(), []byte("x"), "image.bin")
	if err == nil {
		t.Fatal("expected error")
	}
}
