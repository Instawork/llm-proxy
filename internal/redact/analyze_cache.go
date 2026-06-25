package redact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	redis "github.com/redis/go-redis/v9"
)

const (
	analyzeCacheVersion     = "v1"
	defaultAnalyzeCacheTTL  = 5 * time.Minute
	defaultMemoryMaxEntries = 4096
	redisAnalyzeKeyPrefix   = "llm:pii:analyze:" + analyzeCacheVersion + ":"
)

// AnalyzeCache stores Presidio /analyze span lists keyed by analysis text
// hash so unchanged chat blocks skip redundant sidecar calls.
type AnalyzeCache interface {
	Get(ctx context.Context, analysisText string) ([]Span, bool)
	Set(ctx context.Context, analysisText string, spans []Span)
	// GetMulti batch-fetches cache entries. Missing keys are omitted.
	GetMulti(ctx context.Context, analysisTexts []string) map[string][]Span
}

// AnalyzeCacheConfig wires optional in-process and Redis backends.
type AnalyzeCacheConfig struct {
	Enabled          bool
	TTL              time.Duration
	MemoryEnabled    bool
	MemoryMaxEntries int
	RedisEnabled     bool
	Redis            *config.RedisConfig
}

// AnalyzeCacheFingerprint hashes redactor settings that affect /analyze
// results. Bump analyzeCacheVersion when recognizers or masking rules change.
func AnalyzeCacheFingerprint(cfg Config) string {
	types := append([]string(nil), cfg.EntityTypes...)
	sort.Strings(types)
	var b strings.Builder
	b.WriteString(analyzeCacheVersion)
	b.WriteByte(0)
	b.WriteString(cfg.Language)
	b.WriteByte(0)
	fmt.Fprintf(&b, "%.4f", cfg.ScoreThreshold)
	b.WriteByte(0)
	b.WriteString(strings.Join(types, ","))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}

func analyzeCacheKey(fingerprint, analysisText string) string {
	sum := sha256.Sum256([]byte(analysisText))
	return fingerprint + ":" + hex.EncodeToString(sum[:])
}

// NewAnalyzeCache builds a layered cache (memory L1, Redis L2). Returns nil
// when cfg.Enabled is false or no backend is selected. closeFn releases Redis
// resources; it is nil when Redis is not used.
func NewAnalyzeCache(cfg AnalyzeCacheConfig, fingerprint string) (AnalyzeCache, func() error, error) {
	if !cfg.Enabled {
		return nil, nil, nil
	}
	if !cfg.MemoryEnabled && !cfg.RedisEnabled {
		slog.Warn("redact: analyze_cache enabled but memory and redis are both off; cache disabled")
		return nil, nil, nil
	}

	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = defaultAnalyzeCacheTTL
	}

	var layers []AnalyzeCache
	var redisClient *redis.Client

	if cfg.MemoryEnabled {
		maxEntries := cfg.MemoryMaxEntries
		if maxEntries <= 0 {
			maxEntries = defaultMemoryMaxEntries
		}
		layers = append(layers, newMemoryAnalyzeCache(ttl, maxEntries, fingerprint))
	}

	if cfg.RedisEnabled {
		if cfg.Redis == nil {
			return nil, nil, fmt.Errorf("redact: analyze_cache.redis enabled but redis config is missing")
		}
		client, err := newAnalyzeCacheRedisClient(cfg.Redis)
		if err != nil {
			return nil, nil, err
		}
		redisClient = client
		layers = append(layers, newRedisAnalyzeCache(client, ttl, fingerprint))
	}

	var closeFn func() error
	if redisClient != nil {
		closeFn = redisClient.Close
	}

	if len(layers) == 1 {
		return layers[0], closeFn, nil
	}
	return newLayeredAnalyzeCache(layers...), closeFn, nil
}

// AnalyzeCacheConfigFromYAML maps features.pii_redact.analyze_cache.
func AnalyzeCacheConfigFromYAML(yaml config.PIIRedactAnalyzeCacheConfig) AnalyzeCacheConfig {
	ttl := time.Duration(yaml.TTLSeconds) * time.Second
	if yaml.TTLSeconds == 0 && yaml.Enabled {
		ttl = defaultAnalyzeCacheTTL
	}
	return AnalyzeCacheConfig{
		Enabled:          yaml.Enabled,
		TTL:              ttl,
		MemoryEnabled:    yaml.Memory.Enabled,
		MemoryMaxEntries: yaml.Memory.MaxEntries,
		RedisEnabled:     yaml.Redis.Enabled,
		Redis:            yaml.Redis.Redis,
	}
}

func encodeAnalyzeCacheSpans(spans []Span) ([]byte, error) {
	return json.Marshal(spans)
}

func decodeAnalyzeCacheSpans(data []byte) ([]Span, error) {
	var spans []Span
	if err := json.Unmarshal(data, &spans); err != nil {
		return nil, err
	}
	return spans, nil
}

func newAnalyzeCacheRedisClient(r *config.RedisConfig) (*redis.Client, error) {
	url := os.ExpandEnv(r.URL)
	addr := os.ExpandEnv(r.Address)
	password := os.ExpandEnv(r.Password)

	var opts *redis.Options
	if url != "" {
		parsed, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("redact analyze cache redis URL: %w", err)
		}
		opts = parsed
		if addr != "" {
			opts.Addr = addr
		}
		if password != "" {
			opts.Password = password
		}
		if r.DBSet {
			opts.DB = r.DB
		}
	} else {
		if addr == "" {
			addr = "localhost:6379"
		}
		opts = &redis.Options{Addr: addr, Password: password, DB: r.DB}
	}

	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redact analyze cache redis ping: %w", err)
	}
	return client, nil
}
