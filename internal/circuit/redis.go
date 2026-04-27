package circuit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	// Redis key prefixes.  We namespace under `llm:cb:` so the circuit
	// breaker can safely share a Redis instance (and database) with other
	// tenants — notably the Finch app, which also uses this cluster.
	// Anything prefixed with `llm:cb:` is owned by the llm-proxy circuit
	// breaker and may be wiped by operators at any time.
	redisKeyFailures = "llm:cb:failures:" // sorted set of failure timestamps
	redisKeyState    = "llm:cb:state:"    // string: "open" (TTL = cooldown); absence falls back to halfopen marker
	redisKeyHalfOpen = "llm:cb:halfopen:" // marker: while present (and state is absent) the circuit is half-open
	redisKeyProbe    = "llm:cb:probe:"    // short-lived lock for half-open probe slot

	// halfOpenMarkerTTLMultiplier keeps the half-open marker alive long
	// enough to outlive the open-state TTL expiry, so GetState can detect
	// the Open → HalfOpen transition even if no traffic arrives for a
	// while after the cooldown elapses.  If the marker eventually expires
	// with no traffic, the circuit silently falls back to Closed — a safe
	// default that matches MemoryStore's behaviour after a long idle.
	halfOpenMarkerTTLMultiplier = 6
)

// RedisStore is a distributed, Redis-backed circuit breaker store.  It uses a
// sorted set per provider to implement the sliding-window failure counter, and
// a keyed string for open/half-open state with a TTL-based cooldown.
type RedisStore struct {
	cfg Config
	rdb *redis.Client
	log *slog.Logger
}

const redisStorePingTimeout = 2 * time.Second

// NewRedisStore constructs a RedisStore connected to the address in cfg.
//
// Connection inputs are resolved in this order:
//  1. If cfg.RedisURL is non-empty it is parsed via redis.ParseURL and its
//     resulting Options form the baseline.
//  2. Otherwise cfg.RedisAddress seeds the baseline Addr.
//  3. The individual RedisPassword / RedisDB fields, when set, overlay
//     whatever the URL parsed.  This lets us share Finch's Redis URL
//     (from SSM) while pinning a dedicated DB to keep `llm:cb:*` keys
//     isolated.
//
// NewRedisStore validates connectivity with a short PING before returning.
// Steady-state ops still fail-open to StateClosed on Redis errors, so a Redis
// outage after startup can never cascade into a proxy outage.
func NewRedisStore(cfg Config) (*RedisStore, error) {
	cfg = cfg.Defaults()

	var opts *redis.Options
	if cfg.RedisURL != "" {
		parsed, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("circuit.RedisStore: parse redis_url: %w", err)
		}
		opts = parsed
	} else {
		if cfg.RedisAddress == "" {
			return nil, fmt.Errorf("circuit.RedisStore: redis address or url is required")
		}
		opts = &redis.Options{Addr: cfg.RedisAddress}
	}

	// Per-field overlays.  We intentionally only overlay when the caller
	// explicitly set a value — e.g. a password of "" means "don't
	// override", not "force-clear", so operators can supply a URL with
	// embedded credentials and leave RedisPassword unset.
	if cfg.RedisPassword != "" {
		opts.Password = cfg.RedisPassword
	}
	if cfg.RedisDBSet {
		opts.DB = cfg.RedisDB
	}

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(context.Background(), redisStorePingTimeout)
	err := client.Ping(pingCtx).Err()
	cancel()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("circuit.RedisStore: ping redis: %w", err)
	}
	return &RedisStore{cfg: cfg, rdb: client, log: slog.Default()}, nil
}

// Ping issues a bounded Redis PING to verify connectivity.  Intended for
// one-shot startup health checks; callers should treat a non-nil error as
// "Redis is currently unreachable" rather than fatal — the Store itself is
// designed to fail-open on subsequent errors.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

// Close releases the underlying Redis client's pooled connections.  Safe to
// call multiple times; go-redis returns nil on subsequent calls.
func (s *RedisStore) Close() error {
	if s == nil || s.rdb == nil {
		return nil
	}
	return s.rdb.Close()
}

// failuresKey returns the sorted-set key for a provider's failure timestamps.
func (s *RedisStore) failuresKey(provider string) string {
	return redisKeyFailures + provider
}

// stateKey returns the state string key for a provider.
func (s *RedisStore) stateKey(provider string) string {
	return redisKeyState + provider
}

// halfOpenKey returns the half-open marker key for a provider.
func (s *RedisStore) halfOpenKey(provider string) string {
	return redisKeyHalfOpen + provider
}

// probeKey returns the probe-lock key for a provider.
func (s *RedisStore) probeKey(provider string) string {
	return redisKeyProbe + provider
}

// GetState returns the current circuit state.
//
// The Open → HalfOpen transition is driven by TTL expiry: while the circuit
// is open, stateKey holds "open" with a TTL equal to the cooldown, and a
// companion halfOpenKey marker is set with a longer TTL.  When the stateKey
// TTL elapses the marker is still present, so the next GetState returns
// HalfOpen and the transport runs a probe.  If no traffic arrives before the
// marker itself expires the circuit silently falls back to Closed — safe
// default behaviour after a long idle.
func (s *RedisStore) GetState(ctx context.Context, provider string) (State, error) {
	val, err := s.rdb.Get(ctx, s.stateKey(provider)).Result()
	if err != nil && err != redis.Nil {
		// On Redis errors, fail open (treat as closed) to avoid taking down the
		// service because of a Redis blip.
		s.log.Warn("circuit.RedisStore: GetState failed open",
			"provider", provider,
			"error", err,
			"ctx_err", ctx.Err(),
		)
		return StateClosed, nil
	}
	if err == nil {
		switch val {
		case "open":
			return StateOpen, nil
		case "half_open":
			return StateHalfOpen, nil
		}
	}

	// stateKey is absent — consult the half-open marker to distinguish a
	// fresh Closed circuit from one whose cooldown has just elapsed.
	exists, hoErr := s.rdb.Exists(ctx, s.halfOpenKey(provider)).Result()
	if hoErr == nil && exists == 1 {
		return StateHalfOpen, nil
	}
	return StateClosed, nil
}

// luaRecordFailure atomically adds a failure timestamp, prunes old ones, and
// opens the circuit if the threshold is crossed.
//
// KEYS[1] = failures sorted set
// KEYS[2] = state string key
// KEYS[3] = half-open marker key
// ARGV[1] = current unix timestamp (float, for ZADD score)
// ARGV[2] = window cutoff unix timestamp (float, for ZREMRANGEBYSCORE)
// ARGV[3] = threshold (int)
// ARGV[4] = cooldown duration in seconds (int)
// ARGV[5] = window duration in seconds (int)
// ARGV[6] = half-open marker TTL in seconds (int)
//
// Returns: new state string ("closed" | "open" | "half_open")
var luaRecordFailure = redis.NewScript(`
local fkey   = KEYS[1]
local skey   = KEYS[2]
local hkey   = KEYS[3]
local now    = tonumber(ARGV[1])
local cutoff = tonumber(ARGV[2])
local thresh = tonumber(ARGV[3])
local cd     = tonumber(ARGV[4])
local win    = tonumber(ARGV[5])
local hoTTL  = tonumber(ARGV[6])

-- Add the new failure and prune the window.  We keep the sorted set alive
-- for at least one full window so the failure count survives even if no
-- further failures arrive before the cooldown ends.
redis.call('ZADD', fkey, now, tostring(now))
redis.call('ZREMRANGEBYSCORE', fkey, '-inf', cutoff)
local fttl = cd * 2
if win > fttl then fttl = win end
redis.call('EXPIRE', fkey, math.ceil(fttl))

local count = redis.call('ZCARD', fkey)

local cur = redis.call('GET', skey)
if cur == false then cur = 'closed' end

if tonumber(count) >= thresh and cur == 'closed' then
	redis.call('SET', skey, 'open', 'EX', cd)
	redis.call('SET', hkey, '1', 'EX', hoTTL)
	return 'open'
end

return cur
`)

// RecordTerminalFailure records one failure and returns the new state.
func (s *RedisStore) RecordTerminalFailure(ctx context.Context, provider string) (State, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(s.cfg.WindowSeconds)

	res, err := luaRecordFailure.Run(ctx, s.rdb,
		[]string{s.failuresKey(provider), s.stateKey(provider), s.halfOpenKey(provider)},
		now, cutoff,
		s.cfg.FailureThreshold,
		s.cfg.CooldownSeconds,
		s.cfg.WindowSeconds,
		s.cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier,
	).Text()
	if err != nil {
		// On Redis error fail safe: return closed so we don't block traffic.
		s.log.Warn("circuit.RedisStore: luaRecordFailure failed open",
			"provider", provider,
			"script", "luaRecordFailure",
			"redis_client", fmt.Sprintf("%p", s.rdb),
			"error", err,
			"ctx_err", ctx.Err(),
		)
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
	pipe.Del(ctx, s.halfOpenKey(provider))
	pipe.Del(ctx, s.failuresKey(provider))
	pipe.Del(ctx, s.probeKey(provider))
	_, err := pipe.Exec(ctx)
	return err
}

// RecordProbeFailed re-opens the circuit for another full cooldown.
func (s *RedisStore) RecordProbeFailed(ctx context.Context, provider string) error {
	cd := time.Duration(s.cfg.CooldownSeconds) * time.Second
	hoTTL := time.Duration(s.cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier) * time.Second
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.stateKey(provider), "open", cd)
	pipe.Set(ctx, s.halfOpenKey(provider), "1", hoTTL)
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

// probeLockTTL is how long a half-open probe slot stays reserved once a
// caller wins the SetNX race.  A lease-refresher (see KeepProbeAlive)
// periodically extends the TTL while the probe is in flight so we can
// keep the initial TTL modest: short enough that a crashed caller never
// starves the circuit for long, long enough that a slow-but-healthy
// probe still fits inside one refresh interval.
const probeLockTTL = 30 * time.Second

// probeLockRefreshInterval is how often KeepProbeAlive extends the probe
// lock's TTL while the probe is in flight.  Set to probeLockTTL/3 so a
// single missed refresh tick (e.g. a brief Redis hiccup) still leaves a
// full probeLockTTL/3 of headroom before the lock would actually expire.
const probeLockRefreshInterval = probeLockTTL / 3

// TryStartProbe atomically acquires the half-open probe slot.  Returns true
// only if this call won the race; other callers should fast-fail.
func (s *RedisStore) TryStartProbe(ctx context.Context, provider string) bool {
	set, err := s.rdb.SetNX(ctx, s.probeKey(provider), "1", probeLockTTL).Result()
	return err == nil && set
}

// ReleaseProbe drops the probe slot without changing the circuit state.
// This is the right call when a probe did not produce a signal we want to
// credit to the upstream (e.g. the caller's context was cancelled or its
// deadline expired before the upstream responded), because in that case
// the outcome tells us nothing about provider health.  Using SetNX + DEL
// here matches the semantics of RecordSuccess / RecordProbeFailed, which
// both also clear the probe key.
func (s *RedisStore) ReleaseProbe(ctx context.Context, provider string) error {
	return s.rdb.Del(ctx, s.probeKey(provider)).Err()
}

// KeepProbeAlive periodically extends the probe lock's TTL until the
// returned stop function is called.  It is the Redis counterpart to the
// MemoryStore's in-process probeInFlight flag: for the in-memory backend
// there is no TTL so no refresh is required, but Redis imposes a TTL on
// the SetNX lock so a slow-but-healthy probe (e.g. a legitimate long
// LLM generation that exceeds probeLockTTL) could otherwise let a
// second probe win the lock concurrently.
//
// The refresh goroutine uses context.Background() on purpose: if the
// caller's request context is cancelled mid-probe, we do NOT want to
// stop refreshing — the probe is still in flight at the HTTP layer
// until RoundTrip returns, and a premature TTL expiry would allow a
// parallel probe.  Callers must always invoke the returned stop
// function (typically via defer) once the probe completes.
func (s *RedisStore) KeepProbeAlive(provider string) (stop func()) {
	done := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(probeLockRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// Re-assert the TTL.  We use a bounded context rather
				// than Background so the refresh op itself can't hang
				// behind a Redis failure.
				refreshCtx, cancel := context.WithTimeout(context.Background(), probeLockTTL)
				_ = s.rdb.Expire(refreshCtx, s.probeKey(provider), probeLockTTL).Err()
				cancel()
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}
