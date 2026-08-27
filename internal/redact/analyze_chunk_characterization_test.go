package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests pin the behavior behind the August 2026 production incident:
// Presidio's spaCy NER is CPU-bound and effectively single-threaded per
// /analyze call, so one large text field (a ~700 KiB tool-result body was
// the real trigger) takes latency proportional to its full size in a single
// request. Under fail_mode "closed" that timeout became a 503 to callers.
//
// The stub analyzer's per-byte latency and the request deadline are both
// scaled down from real Presidio/production numbers by the same factor, so
// the tests run in milliseconds while preserving the ratio that makes an
// un-chunked 700 KiB field blow the budget.
const (
	characterizationBodyBytes  = 717_000 // ~700 KiB, matches the documented incident payload size
	characterizationPerByte    = time.Microsecond
	characterizationDeadline   = 200 * time.Millisecond
	characterizationChunkChars = 100_000
)

// TestScrub_LargeSingleFieldTimesOutWithoutChunking pins today's default
// (chunking disabled, ChunkChars: 0) behavior: a single ~700 KiB field is
// sent to Presidio in one /analyze call, and that call cannot finish inside
// a deadline sized for a much smaller body.
func TestScrub_LargeSingleFieldTimesOutWithoutChunking(t *testing.T) {
	text := strings.Repeat("a", characterizationBodyBytes)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		time.Sleep(time.Duration(len(payload.Text)) * characterizationPerByte)
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{AnalyzerURL: srv.URL, Timeout: characterizationDeadline})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := WithAnalyzeTimeout(context.Background(), characterizationDeadline)
	if _, err := r.Scrub(ctx, text, NewRegistry()); err == nil {
		t.Fatal("expected a timeout analyzing a 700KB field in one un-chunked /analyze call, got nil error")
	}
}

// TestScrub_LargeSingleFieldSucceedsWithChunking is the flip side: with
// ChunkChars configured, the same field and the same per-byte-latency stub
// complete inside the same deadline because the chunks run in parallel
// instead of one call bearing the full-body latency.
func TestScrub_LargeSingleFieldSucceedsWithChunking(t *testing.T) {
	text := strings.Repeat("a", characterizationBodyBytes)

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		time.Sleep(time.Duration(len(payload.Text)) * characterizationPerByte)
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            characterizationDeadline,
		ChunkChars:         characterizationChunkChars,
		AnalyzeConcurrency: 8,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := WithAnalyzeTimeout(context.Background(), characterizationDeadline)
	if _, err := r.Scrub(ctx, text, NewRegistry()); err != nil {
		t.Fatalf("Scrub with chunking enabled: %v", err)
	}
}
