package redact

import "context"

type analyzeCachePrefetchKey struct{}

// WithAnalyzeCachePrefetch attaches a batch of cache hits for the current
// JSON scrub pass. analyzeSpans consults this before per-block Get calls.
func WithAnalyzeCachePrefetch(ctx context.Context, hits map[string][]Span) context.Context {
	if len(hits) == 0 {
		return ctx
	}
	return context.WithValue(ctx, analyzeCachePrefetchKey{}, hits)
}

func analyzeCachePrefetchFromContext(ctx context.Context) map[string][]Span {
	if v, ok := ctx.Value(analyzeCachePrefetchKey{}).(map[string][]Span); ok {
		return v
	}
	return nil
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
