package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func TestAnalyzeCache_MemoryHitSkipsPresidio(t *testing.T) {
	var calls atomic.Int32
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode([]Span{{
			Start: 0, End: 4, EntityType: "PERSON", Score: 0.9,
		}})
	})

	cache, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
	}, AnalyzeCacheFingerprint(Config{EntityTypes: DefaultEntityTypes, ScoreThreshold: 0.5, Language: "en"}))
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}

	r, err := New(Config{
		AnalyzerURL:    srv.URL,
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
		AnalyzeCache:   cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	text := "Jane said hello"
	if _, err := r.Scrub(ctx, text, NewRegistry()); err != nil {
		t.Fatalf("first Scrub: %v", err)
	}
	if _, err := r.Scrub(ctx, text, NewRegistry()); err != nil {
		t.Fatalf("second Scrub: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("Presidio calls = %d, want 1", got)
	}
}

func TestAnalyzeCache_RedisPopulatesMemory(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	fingerprint := AnalyzeCacheFingerprint(Config{
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
	})
	redisCfg := &config.RedisConfig{Address: mr.Addr(), DBSet: true}

	redisOnly, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:      true,
		TTL:          time.Minute,
		RedisEnabled: true,
		Redis:        redisCfg,
	}, fingerprint)
	if err != nil {
		t.Fatalf("redis cache: %v", err)
	}

	layered, closeFn, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
		RedisEnabled:  true,
		Redis:         redisCfg,
	}, fingerprint)
	if err != nil {
		t.Fatalf("layered cache: %v", err)
	}
	if closeFn != nil {
		defer closeFn()
	}

	spans := []Span{{Start: 0, End: 3, EntityType: "PERSON", Score: 0.95}}
	analysisText := "Bob"
	ctx := context.Background()
	redisOnly.Set(ctx, analysisText, spans)

	got, ok := layered.Get(ctx, analysisText)
	if !ok {
		t.Fatal("expected layered cache hit from redis")
	}
	if len(got) != 1 || got[0].EntityType != "PERSON" {
		t.Fatalf("spans = %#v", got)
	}

	mr.FlushAll()
	got, ok = layered.Get(ctx, analysisText)
	if !ok {
		t.Fatal("expected memory hit after redis flush")
	}
	if len(got) != 1 || got[0].EntityType != "PERSON" {
		t.Fatalf("memory spans = %#v", got)
	}
}

func TestAnalyzeCache_MemoryExpires(t *testing.T) {
	cache := newMemoryAnalyzeCache(50*time.Millisecond, 16, "fp")
	ctx := context.Background()
	cache.Set(ctx, "hello", []Span{{EntityType: "PERSON", Score: 0.9}})
	if _, ok := cache.Get(ctx, "hello"); !ok {
		t.Fatal("expected hit before expiry")
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := cache.Get(ctx, "hello"); ok {
		t.Fatal("expected miss after expiry")
	}
}

func TestAnalyzeCache_RedisClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cache := newRedisAnalyzeCache(client, time.Minute, "abc123")
	ctx := context.Background()
	cache.Set(ctx, "payload", []Span{{EntityType: "EMAIL_ADDRESS", Score: 0.99}})
	got, ok := cache.Get(ctx, "payload")
	if !ok || len(got) != 1 {
		t.Fatalf("redis get = %#v ok=%v", got, ok)
	}
}

func TestAnalyzeCache_RedisGetMulti(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	cache := newRedisAnalyzeCache(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Minute, "fp")
	ctx := context.Background()
	for _, text := range []string{"alpha", "beta", "gamma"} {
		cache.Set(ctx, text, []Span{{EntityType: "PERSON", Score: 0.9}})
	}

	hits := cache.GetMulti(ctx, []string{"alpha", "beta", "gamma", "missing"})
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3: %#v", len(hits), hits)
	}
}

func TestScrubJSON_PrefetchAnalyzeCache(t *testing.T) {
	var calls atomic.Int32
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode([]Span{})
	})

	cache, _, err := NewAnalyzeCache(AnalyzeCacheConfig{
		Enabled:       true,
		TTL:           time.Minute,
		MemoryEnabled: true,
	}, AnalyzeCacheFingerprint(Config{EntityTypes: DefaultEntityTypes, ScoreThreshold: 0.5, Language: "en"}))
	if err != nil {
		t.Fatalf("NewAnalyzeCache: %v", err)
	}

	r, err := New(Config{
		AnalyzerURL:    srv.URL,
		EntityTypes:    DefaultEntityTypes,
		ScoreThreshold: 0.5,
		Language:       "en",
		AnalyzeCache:   cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"messages":[{"role":"user","content":"one"},{"role":"user","content":"two"},{"role":"user","content":"three"}]}`
	ctx := WithProvider(context.Background(), "anthropic")

	if _, err := r.Scrub(ctx, body, NewRegistry()); err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("warm Presidio calls = %d, want 3", got)
	}

	if _, err := r.Scrub(ctx, body, NewRegistry()); err != nil {
		t.Fatalf("cached scrub: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("cached Presidio calls = %d, want 3 (no extra analyze)", got)
	}
}

func TestAnalyzeCacheConfigFromYAML_DefaultTTL(t *testing.T) {
	cfg := AnalyzeCacheConfigFromYAML(config.PIIRedactAnalyzeCacheConfig{Enabled: true, Memory: config.PIIRedactAnalyzeCacheMemoryConfig{Enabled: true}})
	if cfg.TTL != defaultAnalyzeCacheTTL {
		t.Fatalf("TTL = %v, want %v", cfg.TTL, defaultAnalyzeCacheTTL)
	}
}
