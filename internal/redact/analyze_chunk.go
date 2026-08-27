package redact

import (
	"context"
	"sort"
	"sync"
)

// analyzeChunkOverlapChars is the rune overlap between adjacent chunks so an
// entity that would otherwise straddle a chunk boundary (e.g. a name split
// across two chunks) is still captured whole by at least one of the two
// overlapping views. dedupeOverlapSpans then collapses the resulting
// duplicate detections in the overlap window.
const analyzeChunkOverlapChars = 200

// analyzeChunked calls /analyze on text, splitting it into parallel chunks
// first when it exceeds Config.ChunkChars runes. Chunking is transparent to
// callers: the merged span list uses the same offsets as a single
// whole-text /analyze call would have returned.
//
// Presidio's spaCy NER is CPU-bound and effectively single-threaded per
// request, so one large field pins a single sidecar worker for the whole
// call regardless of how many workers the sidecar runs. Splitting the field
// lets the existing AnalyzeConcurrency fan-out (see json_scrub.go) bound the
// wall-clock time by parallelism instead of by the field's total size. See
// the August 2026 fail_mode "closed" 503 incident on large tool-result
// bodies.
func (r *Redactor) analyzeChunked(ctx context.Context, text string) ([]Span, error) {
	if r.cfg.ChunkChars <= 0 {
		return r.analyze(ctx, text, nil, r.cfg.ScoreThreshold)
	}

	runes := []rune(text)
	if len(runes) <= r.cfg.ChunkChars {
		return r.analyze(ctx, text, nil, r.cfg.ScoreThreshold)
	}

	chunks := chunkRunes(runes, r.cfg.ChunkChars, analyzeChunkOverlapChars)
	if len(chunks) <= 1 {
		return r.analyze(ctx, text, nil, r.cfg.ScoreThreshold)
	}

	// AnalyzeTimeoutFromContext holds the deadline the middleware computed
	// for this whole request (scaled by total body size). Binding it to an
	// absolute deadline once, here, means every chunk's analyze() call —
	// which independently calls AnalyzeTimeoutFromContext again — shares
	// that one deadline instead of each restarting a fresh full-length
	// timeout from its own start time. Without this, N sequential waves of
	// chunks could take up to N times the intended budget.
	overallTimeout := AnalyzeTimeoutFromContext(ctx, r.cfg.Timeout)
	limit := r.analyzeConcurrency()
	ctx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()

	// A fixed pool of workers pulls chunk indexes off jobs, capping goroutine
	// count at AnalyzeConcurrency. A goroutine-per-chunk design would let a
	// pathologically small analyze_chunk_chars (e.g. 1) turn a multi-MB field
	// into millions of goroutines blocked on a semaphore, exhausting memory
	// before the shared deadline even expires.
	workers := limit
	if workers > len(chunks) {
		workers = len(chunks)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	perChunkSpans := make([][]Span, len(chunks))

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := range jobs {
				spans, err := r.analyze(ctx, chunks[i].text, nil, r.cfg.ScoreThreshold)
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					continue
				}
				offset := chunks[i].offset
				rebased := make([]Span, len(spans))
				for j, s := range spans {
					s.Start += offset
					s.End += offset
					rebased[j] = s
				}
				perChunkSpans[i] = rebased
			}
		}()
	}

feed:
	for i := range chunks {
		select {
		case jobs <- i:
		case <-ctx.Done():
			mu.Lock()
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			mu.Unlock()
			break feed
		}
	}
	close(jobs)

	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	var merged []Span
	for _, spans := range perChunkSpans {
		merged = append(merged, spans...)
	}
	return dedupeOverlapSpans(merged), nil
}

// textChunk is a rune-offset view into the text passed to analyzeChunked.
type textChunk struct {
	text   string
	offset int // rune offset into the original text
}

// chunkRunes splits runes into overlapping segments no longer than maxChars,
// preferring to break at whitespace near the boundary so word-level
// entities (PERSON, EMAIL_ADDRESS) aren't sliced in half. Each chunk after
// the first begins overlapChars runes before its nominal boundary.
func chunkRunes(runes []rune, maxChars, overlapChars int) []textChunk {
	if len(runes) == 0 || maxChars <= 0 {
		return nil
	}
	if overlapChars < 0 || overlapChars >= maxChars {
		overlapChars = 0
	}

	var chunks []textChunk
	start := 0
	for start < len(runes) {
		end := start + maxChars
		if end >= len(runes) {
			end = len(runes)
		} else {
			end = whitespaceSplitPoint(runes, start, end)
		}
		chunks = append(chunks, textChunk{text: string(runes[start:end]), offset: start})
		if end >= len(runes) {
			break
		}
		next := end - overlapChars
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

// whitespaceSplitPoint looks backward from end (bounded to a small lookback
// window within [start, end]) for the nearest whitespace rune, so a chunk
// boundary doesn't land mid-word. Falls back to end when none is found.
func whitespaceSplitPoint(runes []rune, start, end int) int {
	const maxLookback = 200
	floor := end - maxLookback
	if floor < start {
		floor = start
	}
	for i := end; i > floor; i-- {
		if isJSONWhitespace(runes[i-1]) {
			return i
		}
	}
	return end
}

// dedupeOverlapSpans merges duplicate detections in the overlap window
// between adjacent chunks: spans of the same entity type with overlapping
// [Start,End) ranges collapse to their union range, keeping the highest
// score. Adjacent chunks can disagree on an entity's exact boundaries (e.g.
// [10,20) at 0.6 from one chunk's view vs [15,25) at 0.9 from the
// overlapping view); taking only the higher-scored span's range would drop
// the [10,15) prefix from redaction, so the merged span must cover both.
func dedupeOverlapSpans(spans []Span) []Span {
	if len(spans) < 2 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].Start != spans[j].Start {
			return spans[i].Start < spans[j].Start
		}
		return spans[i].End < spans[j].End
	})
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if i := indexOfOverlappingSameType(out, s); i >= 0 {
			if s.Start < out[i].Start {
				out[i].Start = s.Start
			}
			if s.End > out[i].End {
				out[i].End = s.End
			}
			if s.Score > out[i].Score {
				out[i].Score = s.Score
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

func indexOfOverlappingSameType(spans []Span, s Span) int {
	for i, existing := range spans {
		if existing.EntityType != s.EntityType {
			continue
		}
		if s.Start < existing.End && existing.Start < s.End {
			return i
		}
	}
	return -1
}
