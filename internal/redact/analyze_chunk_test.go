package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAnalyzeChunked_SharesOneOverallDeadlineAcrossChunks guards against a
// regression where each chunk's analyze() call gets its own fresh copy of
// the request's timeout budget instead of all chunks sharing one absolute
// deadline. With AnalyzeConcurrency=1 forcing five 80ms chunk calls to run
// strictly sequentially, a per-chunk-reset bug would let the whole call run
// close to 5*80ms=400ms; sharing one deadline must cut it off near the
// 150ms budget instead.
func TestAnalyzeChunked_SharesOneOverallDeadlineAcrossChunks(t *testing.T) {
	const perChunkLatency = 80 * time.Millisecond
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(perChunkLatency)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{
		AnalyzerURL:        srv.URL,
		Timeout:            perChunkLatency,
		ChunkChars:         10,
		AnalyzeConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	text := strings.Repeat("a", 50) // 5 chunks of 10 chars, no whitespace to split on
	ctx := WithAnalyzeTimeout(context.Background(), 150*time.Millisecond)

	start := time.Now()
	_, err = r.Scrub(ctx, text, NewRegistry())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected the shared deadline to expire before all chunks finished, got success in %v", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("took %v; a per-chunk-reset timeout bug would let this run close to 400ms instead of stopping near the 150ms shared deadline", elapsed)
	}
}

func TestChunkRunes_SingleChunkWhenUnderLimit(t *testing.T) {
	chunks := chunkRunes([]rune("hello world"), 100, 10)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].offset != 0 || chunks[0].text != "hello world" {
		t.Fatalf("unexpected chunk: %+v", chunks[0])
	}
}

func TestChunkRunes_SplitsAtWhitespaceNearBoundary(t *testing.T) {
	text := "aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd"
	chunks := chunkRunes([]rune(text), 15, 0)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c.text) == 0 {
			t.Fatalf("empty chunk at offset %d", c.offset)
		}
		if len(c.text) > 0 && (c.text[0] != ' ' && len(c.text) < len(text)) {
			// Boundaries should land on whitespace, not mid-word, except
			// for the final chunk which just takes whatever remains.
			last := c.text[len(c.text)-1]
			if last != ' ' && c.offset+len(c.text) != len(text) {
				t.Fatalf("chunk %q does not end on a whitespace boundary", c.text)
			}
		}
	}
}

func TestChunkRunes_OverlapCarriesBoundaryText(t *testing.T) {
	text := "0123456789" + "0123456789" + "0123456789" // 30 chars, no whitespace
	chunks := chunkRunes([]rune(text), 12, 4)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].offset >= chunks[i-1].offset+12 {
			t.Fatalf("chunk %d offset %d does not overlap with previous chunk", i, chunks[i].offset)
		}
	}
}

func TestChunkRunes_EmptyInput(t *testing.T) {
	if chunks := chunkRunes(nil, 10, 2); chunks != nil {
		t.Fatalf("expected nil chunks for empty input, got %v", chunks)
	}
}

func TestDedupeOverlapSpans_CollapsesOverlappingSameType(t *testing.T) {
	spans := []Span{
		{Start: 10, End: 20, EntityType: "PERSON", Score: 0.6},
		{Start: 15, End: 25, EntityType: "PERSON", Score: 0.9},
	}
	got := dedupeOverlapSpans(spans)
	if len(got) != 1 {
		t.Fatalf("expected 1 merged span, got %d (%+v)", len(got), got)
	}
	if got[0].Start != 10 || got[0].End != 25 {
		t.Fatalf("expected the merged span to cover the union range [10,25), got %+v", got[0])
	}
	if got[0].Score != 0.9 {
		t.Fatalf("expected the higher score to survive, got %+v", got[0])
	}
}

func TestDedupeOverlapSpans_KeepsNonOverlappingAndDifferentTypes(t *testing.T) {
	spans := []Span{
		{Start: 0, End: 5, EntityType: "PERSON", Score: 0.8},
		{Start: 5, End: 10, EntityType: "EMAIL_ADDRESS", Score: 0.7},
		{Start: 100, End: 110, EntityType: "PERSON", Score: 0.5},
	}
	got := dedupeOverlapSpans(spans)
	if len(got) != 3 {
		t.Fatalf("expected 3 spans preserved, got %d (%+v)", len(got), got)
	}
}
