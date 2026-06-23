package adminrollup

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

var hincrbyfloatScript = redis.NewScript(`
local newVal = redis.call("HINCRBYFLOAT", KEYS[1], ARGV[1], ARGV[2])
redis.call("EXPIRE", KEYS[1], tonumber(ARGV[3]))
return newVal
`)

// Backend kinds.
const (
	BackendRedis  = "redis"
	BackendMemory = "memory"
)

// backend abstracts the key/value store behind the rollup Store. Both the
// Redis and in-memory implementations satisfy it, mirroring the adapter
// pattern used by the circuit breaker package (circuit.Store + Factory).
//
// Keys are opaque, fully-qualified strings built by the Store; values are
// pre-marshalled JSON blobs. A ttl of 0 means "no expiry".
type backend interface {
	set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	del(ctx context.Context, key string) error
	// mget returns one entry per requested key, in order; a nil entry means
	// the key is absent (or expired).
	mget(ctx context.Context, keys []string) ([]*string, error)
	applyDelta(ctx context.Context, metric, day string, d Delta, ttl time.Duration) error
	hgetall(ctx context.Context, key string) (map[string]float64, error)
	// hget returns a single numeric hash field. A missing key/field yields
	// (0, nil) — absence is not an error for read-through callers.
	hget(ctx context.Context, key, field string) (float64, error)
	// hincrbyfloat atomically increments a hash field and sets key TTL.
	hincrbyfloat(ctx context.Context, key, field string, delta float64, ttl time.Duration) error
	trySetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	// reserveUnderLimit atomically reserves estimate against (recorded spend +
	// outstanding reservations) vs limitCents. Returns true when reserved.
	reserveUnderLimit(ctx context.Context, spendHashKey, spendField, reservedHashKey, reservedField string, estimate float64, limitCents int64, ttl time.Duration) (bool, error)
	// addReserved adjusts a reservation field by delta (floored at 0).
	addReserved(ctx context.Context, reservedHashKey, reservedField string, delta float64, ttl time.Duration) error
	appendRecentEvent(ctx context.Context, key string, payload []byte, maxLen int, ttl time.Duration) error
	loadRecentEvents(ctx context.Context, key string, maxLen int) ([]byte, error)
	close() error
	kind() string
}

// newBackend selects the rollup backend from config. An explicit
// cfg.Redis.Backend wins; otherwise it infers redis when a Redis
// address/URL is present, else memory.
func newBackend(cfg Config) (backend, error) {
	kind := cfg.Backend
	if kind == "" {
		if cfg.Redis != nil && (cfg.Redis.URL != "" || cfg.Redis.Address != "") {
			kind = BackendRedis
		} else {
			kind = BackendMemory
		}
	}

	switch kind {
	case BackendMemory:
		return newMemoryBackend(), nil
	case BackendRedis:
		if cfg.Redis == nil {
			return nil, fmt.Errorf("admin rollups: redis backend selected but redis config is missing")
		}
		client, err := newRedisClient(cfg.Redis)
		if err != nil {
			return nil, err
		}
		return &redisBackend{rdb: client}, nil
	default:
		return nil, fmt.Errorf("admin rollups: unknown backend %q", kind)
	}
}

// --- Redis backend ----------------------------------------------------------

type redisBackend struct {
	rdb *redis.Client
}

func (b *redisBackend) set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return b.rdb.Set(ctx, key, value, ttl).Err()
}

func (b *redisBackend) del(ctx context.Context, key string) error {
	return b.rdb.Del(ctx, key).Err()
}

func (b *redisBackend) applyDelta(ctx context.Context, metric, day string, d Delta, ttl time.Duration) error {
	return applyDeltaRedis(ctx, b.rdb, metric, day, d, ttl)
}

func (b *redisBackend) hgetall(ctx context.Context, key string) (map[string]float64, error) {
	m, err := b.rdb.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	return hgetallFloat(m), nil
}

func (b *redisBackend) hget(ctx context.Context, key, field string) (float64, error) {
	v, err := b.rdb.HGet(ctx, key, field).Float64()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return v, nil
}

func (b *redisBackend) hincrbyfloat(ctx context.Context, key, field string, delta float64, ttl time.Duration) error {
	ttlSec := int64(ttl / time.Second)
	if ttl > 0 && ttlSec < 1 {
		ttlSec = 1
	}
	_, err := hincrbyfloatScript.Run(
		ctx,
		b.rdb,
		[]string{key},
		field,
		strconv.FormatFloat(delta, 'f', -1, 64),
		ttlSec,
	).Result()
	return err
}

func (b *redisBackend) trySetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	return b.rdb.SetNX(ctx, key, value, ttl).Result()
}

func (b *redisBackend) mget(ctx context.Context, keys []string) ([]*string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	vals, err := b.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]*string, len(vals))
	for i, raw := range vals {
		if s, ok := raw.(string); ok && s != "" {
			v := s
			out[i] = &v
		}
	}
	return out, nil
}

func (b *redisBackend) close() error { return b.rdb.Close() }
func (b *redisBackend) kind() string { return BackendRedis }

// --- In-memory backend ------------------------------------------------------

type memEntry struct {
	value     string
	expiresAt time.Time // zero == no expiry
}

type memHash map[string]float64

// memoryBackend is a process-local store with lazy TTL expiry. It gives local
// dev and tests the full rollup feature (daily_history) without a running
// Redis; it intentionally does not survive process restarts.
type memoryBackend struct {
	mu    sync.Mutex
	data  map[string]memEntry
	hash  map[string]memHash
	lists map[string][]string
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{
		data:  make(map[string]memEntry),
		hash:  make(map[string]memHash),
		lists: make(map[string][]string),
	}
}

func (b *memoryBackend) applyDelta(_ context.Context, metric, day string, d Delta, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	applyToHash := func(key string, fields map[string]float64) {
		if len(fields) == 0 {
			return
		}
		h := b.hash[key]
		if h == nil {
			h = make(memHash)
			b.hash[key] = h
		}
		for f, v := range fields {
			h[f] += v
		}
		b.data[key] = memEntry{value: "hash", expiresAt: exp}
	}
	applyToHash(totalsKey(metric, day), d.Totals)
	for dim, members := range d.Dimensions {
		applyToHash(dimKey(metric, day, dim), members)
	}
	return nil
}

func (b *memoryBackend) hgetall(_ context.Context, key string) (map[string]float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.hash[key]
	if h == nil {
		return nil, nil
	}
	out := make(map[string]float64, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out, nil
}

func (b *memoryBackend) hget(_ context.Context, key, field string) (float64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h := b.hash[key]; h != nil {
		return h[field], nil
	}
	return 0, nil
}

func (b *memoryBackend) hincrbyfloat(_ context.Context, key, field string, delta float64, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.hash[key]
	if h == nil {
		h = make(memHash)
		b.hash[key] = h
	}
	h[field] += delta
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	b.data[key] = memEntry{value: "hash", expiresAt: exp}
	return nil
}

func (b *memoryBackend) trySetNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.data[key]; ok {
		return false, nil
	}
	entry := memEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	b.data[key] = entry
	return true, nil
}

func (b *memoryBackend) set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := memEntry{value: string(value)}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}
	b.data[key] = entry
	return nil
}

func (b *memoryBackend) del(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
	return nil
}

func (b *memoryBackend) mget(_ context.Context, keys []string) ([]*string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	out := make([]*string, len(keys))
	for i, key := range keys {
		entry, ok := b.data[key]
		if !ok {
			continue
		}
		if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
			delete(b.data, key)
			continue
		}
		v := entry.value
		out[i] = &v
	}
	return out, nil
}

func (b *memoryBackend) close() error { return nil }
func (b *memoryBackend) kind() string { return BackendMemory }
