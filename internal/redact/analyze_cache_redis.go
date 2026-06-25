package redact

import (
	"context"
	"log/slog"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const redisMGetChunkSize = 128

type redisAnalyzeCache struct {
	rdb         *redis.Client
	ttl         time.Duration
	fingerprint string
}

func newRedisAnalyzeCache(rdb *redis.Client, ttl time.Duration, fingerprint string) *redisAnalyzeCache {
	return &redisAnalyzeCache{
		rdb:         rdb,
		ttl:         ttl,
		fingerprint: fingerprint,
	}
}

func (c *redisAnalyzeCache) redisKey(analysisText string) string {
	return redisAnalyzeKeyPrefix + analyzeCacheKey(c.fingerprint, analysisText)
}

func (c *redisAnalyzeCache) Get(ctx context.Context, analysisText string) ([]Span, bool) {
	data, err := c.rdb.Get(ctx, c.redisKey(analysisText)).Bytes()
	if err != nil {
		if err != redis.Nil {
			slog.Debug("redact: analyze cache redis get failed", "error", err)
		}
		return nil, false
	}
	spans, err := decodeAnalyzeCacheSpans(data)
	if err != nil {
		slog.Debug("redact: analyze cache redis decode failed", "error", err)
		return nil, false
	}
	return spans, true
}

func (c *redisAnalyzeCache) GetMulti(ctx context.Context, analysisTexts []string) map[string][]Span {
	unique := uniqueAnalysisTexts(analysisTexts)
	if len(unique) == 0 {
		return nil
	}

	keys := make([]string, len(unique))
	textByKey := make(map[string]string, len(unique))
	for i, text := range unique {
		key := c.redisKey(text)
		keys[i] = key
		textByKey[key] = text
	}

	hits := make(map[string][]Span)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, defaultAnalyzeConcurrency)

	for start := 0; start < len(keys); start += redisMGetChunkSize {
		end := start + redisMGetChunkSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]

		wg.Add(1)
		go func(chunk []string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}

			vals, err := c.rdb.MGet(ctx, chunk...).Result()
			if err != nil {
				if err != redis.Nil {
					slog.Debug("redact: analyze cache redis mget failed", "error", err)
				}
				return
			}

			local := make(map[string][]Span)
			for i, val := range vals {
				if val == nil {
					continue
				}
				raw, ok := val.(string)
				if !ok {
					continue
				}
				spans, err := decodeAnalyzeCacheSpans([]byte(raw))
				if err != nil {
					continue
				}
				local[textByKey[chunk[i]]] = spans
			}
			if len(local) == 0 {
				return
			}
			mu.Lock()
			for text, spans := range local {
				hits[text] = spans
			}
			mu.Unlock()
		}(chunk)
	}

	wg.Wait()
	if len(hits) == 0 {
		return nil
	}
	return hits
}

func (c *redisAnalyzeCache) Set(ctx context.Context, analysisText string, spans []Span) {
	data, err := encodeAnalyzeCacheSpans(spans)
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, c.redisKey(analysisText), data, c.ttl).Err(); err != nil {
		slog.Debug("redact: analyze cache redis set failed", "error", err)
	}
}
