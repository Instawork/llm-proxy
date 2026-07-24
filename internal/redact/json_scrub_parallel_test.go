package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

// TestScrubJSON_ParallelSiblingStringsInSameMap covers two scrub-eligible
// strings under the SAME parent map ("system" via the Anthropic adapter and
// root-level "prompt" via the OpenAI adapter). Their setText callbacks write
// into one shared map; regression: the parallel path used to apply them
// outside the mutex, racing on the map (caught by -race; panics with
// "concurrent map writes" in production).
func TestScrubJSON_ParallelSiblingStringsInSameMap(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(10 * time.Millisecond)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{AnalyzerURL: srv.URL, AnalyzeConcurrency: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"system":"block one","prompt":"block two"}`
	res, err := r.Scrub(context.Background(), body, NewRegistry())
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	for _, want := range []string{"block one", "block two"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("scrubbed output lost %q: %s", want, res.Text)
		}
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
