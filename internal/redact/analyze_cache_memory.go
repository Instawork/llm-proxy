package redact

import (
	"context"
	"sync"
	"time"
)

type memoryAnalyzeCache struct {
	mu          sync.RWMutex
	ttl         time.Duration
	maxEntries  int
	fingerprint string
	entries     map[string]memoryCacheEntry
}

type memoryCacheEntry struct {
	spans   []Span
	expires time.Time
}

func newMemoryAnalyzeCache(ttl time.Duration, maxEntries int, fingerprint string) *memoryAnalyzeCache {
	return &memoryAnalyzeCache{
		ttl:         ttl,
		maxEntries:  maxEntries,
		fingerprint: fingerprint,
		entries:     make(map[string]memoryCacheEntry),
	}
}

func (c *memoryAnalyzeCache) cacheKey(analysisText string) string {
	return analyzeCacheKey(c.fingerprint, analysisText)
}

func (c *memoryAnalyzeCache) Get(_ context.Context, analysisText string) ([]Span, bool) {
	key := c.cacheKey(analysisText)
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expires) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, false
	}
	return cloneSpans(entry.spans), true
}

func (c *memoryAnalyzeCache) Set(_ context.Context, analysisText string, spans []Span) {
	key := c.cacheKey(analysisText)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}
	c.entries[key] = memoryCacheEntry{
		spans:   cloneSpans(spans),
		expires: time.Now().Add(c.ttl),
	}
}

func (c *memoryAnalyzeCache) GetMulti(ctx context.Context, analysisTexts []string) map[string][]Span {
	hits := make(map[string][]Span)
	for _, text := range uniqueAnalysisTexts(analysisTexts) {
		if err := ctx.Err(); err != nil {
			break
		}
		if spans, ok := c.Get(ctx, text); ok {
			hits[text] = spans
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return hits
}

func (c *memoryAnalyzeCache) evictLocked() {
	now := time.Now()
	for k, entry := range c.entries {
		if now.After(entry.expires) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) < c.maxEntries {
		return
	}
	for k := range c.entries {
		delete(c.entries, k)
		break
	}
}

func cloneSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	out := make([]Span, len(spans))
	copy(out, spans)
	return out
}
