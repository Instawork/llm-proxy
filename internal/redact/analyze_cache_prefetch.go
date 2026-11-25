package redact

import "context"

type analyzeCachePrefetchKey struct{}

type analyzeCachePrefetchState struct {
	hits    map[string][]Span
	checked map[string]struct{}
}

// WithAnalyzeCachePrefetch attaches a batch of cache hits for the current
// JSON scrub pass. analyzeSpans consults this before per-block Get calls.
func WithAnalyzeCachePrefetch(ctx context.Context, hits map[string][]Span) context.Context {
	if len(hits) == 0 {
		return ctx
	}
	return context.WithValue(ctx, analyzeCachePrefetchKey{}, analyzeCachePrefetchState{hits: hits})
}

func withAnalyzeCachePrefetchChecked(
	ctx context.Context,
	hits map[string][]Span,
	checked map[string]struct{},
) context.Context {
	if len(hits) == 0 && len(checked) == 0 {
		return ctx
	}
	return context.WithValue(ctx, analyzeCachePrefetchKey{}, analyzeCachePrefetchState{
		hits:    hits,
		checked: checked,
	})
}

func analyzeCachePrefetchFromContext(ctx context.Context) analyzeCachePrefetchState {
	if v, ok := ctx.Value(analyzeCachePrefetchKey{}).(analyzeCachePrefetchState); ok {
		return v
	}
	return analyzeCachePrefetchState{}
}

func uniqueAnalysisTexts(texts []string) []string {
	if len(texts) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(texts))
	out := make([]string, 0, len(texts))
	for _, text := range texts {
		if text == "" {
			continue
		}
		if _, ok := seen[text]; ok {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}

func analysisTextsWithoutHits(texts []string, hits map[string][]Span) []string {
	if len(hits) == 0 {
		return uniqueAnalysisTexts(texts)
	}
	seen := make(map[string]struct{})
	var out []string
	for _, text := range texts {
		if text == "" {
			continue
		}
		if _, hit := hits[text]; hit {
			continue
		}
		if _, dup := seen[text]; dup {
			continue
		}
		seen[text] = struct{}{}
		out = append(out, text)
	}
	return out
}
