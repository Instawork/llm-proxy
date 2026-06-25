package redact

import "context"

type layeredAnalyzeCache struct {
	layers []AnalyzeCache
}

func newLayeredAnalyzeCache(layers ...AnalyzeCache) *layeredAnalyzeCache {
	return &layeredAnalyzeCache{layers: layers}
}

func (c *layeredAnalyzeCache) Get(ctx context.Context, analysisText string) ([]Span, bool) {
	for i, layer := range c.layers {
		spans, ok := layer.Get(ctx, analysisText)
		if !ok {
			continue
		}
		for j := 0; j < i; j++ {
			c.layers[j].Set(ctx, analysisText, spans)
		}
		return spans, true
	}
	return nil, false
}

func (c *layeredAnalyzeCache) GetMulti(ctx context.Context, analysisTexts []string) map[string][]Span {
	hits := make(map[string][]Span)
	remaining := uniqueAnalysisTexts(analysisTexts)
	for i, layer := range c.layers {
		if len(remaining) == 0 {
			break
		}
		layerHits := layer.GetMulti(ctx, remaining)
		for text, spans := range layerHits {
			hits[text] = spans
		}
		for j := 0; j < i; j++ {
			for text, spans := range layerHits {
				c.layers[j].Set(ctx, text, spans)
			}
		}
		remaining = analysisTextsWithoutHits(remaining, hits)
	}
	if len(hits) == 0 {
		return nil
	}
	return hits
}

func (c *layeredAnalyzeCache) Set(ctx context.Context, analysisText string, spans []Span) {
	for _, layer := range c.layers {
		layer.Set(ctx, analysisText, spans)
	}
}
