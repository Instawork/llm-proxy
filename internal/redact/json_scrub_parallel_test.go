package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestScrubJSON_ParallelizesMultipleContentBlocks(t *testing.T) {
	const delay = 120 * time.Millisecond
	var peak int32
	var active int32

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(delay)
		atomic.AddInt32(&active, -1)

		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{AnalyzerURL: srv.URL, AnalyzeConcurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body, err := json.Marshal(map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "block one"},
			map[string]any{"role": "assistant", "content": "block two"},
			map[string]any{"role": "user", "content": "block three"},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	start := time.Now()
	if _, err := r.Scrub(context.Background(), string(body), NewRegistry()); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	elapsed := time.Since(start)

	if peak < 2 {
		t.Fatalf("expected concurrent /analyze calls, peak active = %d", peak)
	}
	if elapsed >= 3*delay {
		t.Fatalf("parallel scrub took %v, expected well under sequential %v", elapsed, 3*delay)
	}
}

func TestScrubJSON_SequentialWhenConcurrencyOne(t *testing.T) {
	var peak int32
	var active int32

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		cur := atomic.AddInt32(&active, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{AnalyzerURL: srv.URL, AnalyzeConcurrency: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`
	if _, err := r.Scrub(context.Background(), body, NewRegistry()); err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if peak != 1 {
		t.Fatalf("concurrency=1 should run sequentially, peak active = %d", peak)
	}
}
