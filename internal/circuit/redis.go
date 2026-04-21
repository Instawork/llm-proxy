package circuit

import (
	"context"
	"fmt"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	// Redis key prefixes.
	redisKeyFailures = "cb:failures:" // sorted set of failure timestamps
	redisKeyState    = "cb:state:"    // string: "open" | "half_open" (absence = closed)
	redisKeyProbe    = "cb:probe:"    // short-lived lock for half-open probe slot
)

// RedisStore is a distributed, Redis-backed circuit breaker store.  It uses a
// sorted set per provider to implement the sliding-window failure counter, and
// a keyed string for open/half-open state with a TTL-based cooldown.
type RedisStore struct {
	cfg Config
	rdb *redis.Client
}

// NewRedisStore constructs a RedisStore connected to the address in cfg.
func NewRedisStore(cfg Config) (*RedisStore, error) {
	cfg = cfg.Defaults()
	if cfg.RedisAddress == "" {
		return nil, fmt.Errorf("circuit.RedisStore: redis address is required")
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddress,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	return &RedisStore{cfg: cfg, rdb: client}, nil
}

// failuresKey returns the sorted-set key for a provider's failure timestamps.
func (s *RedisStore) failuresKey(provider string) string {
	return redisKeyFailures + provider
}

// stateKey returns the state string key for a provider.
func (s *RedisStore) stateKey(provider string) string {
	return redisKeyState + provider
}

// probeKey returns the probe-lock key for a provider.
func (s *RedisStore) probeKey(provider string) string {
	return redisKeyProbe + provider
}

// GetState returns the current circuit state, honouring the TTL-based
// cooldown (Open state key expires → transitions to HalfOpen).
func (s *RedisStore) GetState(ctx context.Context, provider string) (State, error) {
	val, err := s.rdb.Get(ctx, s.stateKey(provider)).Result()
	if err == redis.Nil {
		return StateClosed, nil
	}
	if err != nil {
		// On Redis errors, fail open (treat as closed) to avoid taking down the
		// service because of a Redis blip.
		return StateClosed, nil
	}
	switch val {
	case "open":
		return StateOpen, nil
	case "half_open":
		return StateHalfOpen, nil
	default:
		return StateClosed, nil
	}
}

// luaRecordFailure atomically adds a failure timestamp, prunes old ones, and
// opens the circuit if the threshold is crossed.
//
// KEYS[1] = failures sorted set
// KEYS[2] = state string key
// ARGV[1] = current unix timestamp (float, for ZADD score)
// ARGV[2] = window cutoff unix timestamp (float, for ZREMRANGEBYSCORE)
// ARGV[3] = threshold (int)
// ARGV[4] = cooldown duration in seconds (int)
//
// Returns: new state string ("closed" | "open")
var luaRecordFailure = redis.NewScript(`
local fkey   = KEYS[1]
local skey   = KEYS[2]
local now    = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local thresh = tonumber(ARGV[3])
local cd     = tonumber(ARGV[4])

-- Add the new failure and prune the window.
redis.call('ZADD', fkey, now, tostring(now))
redis.call('ZREMRANGEBYSCORE', fkey, '-inf', cutoff)
redis.call('EXPIRE', fkey, math.ceil(tonumber(ARGV[4]) * 2))

local count = redis.call('ZCARD', fkey)

-- Check current state; only open if currently closed.
local cur = redis.call('GET', skey)
if cur == false then cur = 'closed' end

if tonumber(count) >= thresh and cur == 'closed' then
	redis.call('SET', skey, 'open', 'EX', cd)
	return 'open'
end

return cur
`)

// RecordTerminalFailure records one failure and returns the new state.
func (s *RedisStore) RecordTerminalFailure(ctx context.Context, provider string) (State, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(s.cfg.WindowSeconds)

	res, err := luaRecordFailure.Run(ctx, s.rdb,
		[]string{s.failuresKey(provider), s.stateKey(provider)},
		now, cutoff, s.cfg.FailureThreshold, s.cfg.CooldownSeconds,
	).Text()
	if err != nil {
		// On Redis error fail safe: return closed so we don't block traffic.
		return StateClosed, nil
	}
	switch res {
	case "open":
		return StateOpen, nil
	case "half_open":
		return StateHalfOpen, nil
	default:
		return StateClosed, nil
	}
}

// RecordSuccess closes the circuit and removes all failure history.
func (s *RedisStore) RecordSuccess(ctx context.Context, provider string) error {
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, s.stateKey(provider))
	pipe.Del(ctx, s.failuresKey(provider))
	pipe.Del(ctx, s.probeKey(provider))
	_, err := pipe.Exec(ctx)
	return err
}

// RecordProbeFailed re-opens the circuit for another full cooldown.
func (s *RedisStore) RecordProbeFailed(ctx context.Context, provider string) error {
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.stateKey(provider), "open",
		time.Duration(s.cfg.CooldownSeconds)*time.Second)
	pipe.Del(ctx, s.probeKey(provider))
	_, err := pipe.Exec(ctx)
	return err
}

// GetStats returns a snapshot of circuit stats for the provider.
func (s *RedisStore) GetStats(ctx context.Context, provider string) (*ProviderStats, error) {
	state, err := s.GetState(ctx, provider)
	if err != nil {
		return nil, err
	}
	stats := &ProviderStats{State: state}

	// Count failures in the current window.
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(s.cfg.WindowSeconds)
	count, err := s.rdb.ZCount(ctx, s.failuresKey(provider),
		fmt.Sprintf("%f", cutoff), "+inf").Result()
	if err == nil {
		stats.Failures = int(count)
	}

	// If open, try to surface the cooldown expiry from the key TTL.
	if state == StateOpen {
		dur, err := s.rdb.TTL(ctx, s.stateKey(provider)).Result()
		if err == nil && dur > 0 {
			t := time.Now().Add(dur)
			stats.CooldownUntil = &t
		}
	}

	return stats, nil
}

// TryStartProbe atomically acquires the half-open probe slot.  Returns true
// only if this call won the race; other callers should fast-fail.
func (s *RedisStore) TryStartProbe(ctx context.Context, provider string) bool {
	set, err := s.rdb.SetNX(ctx, s.probeKey(provider), "1",
		5*time.Second).Result()
	return err == nil && set
}
