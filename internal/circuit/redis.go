package circuit

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	// Redis key prefixes.  We namespace under `llm:cb:` so the circuit
	// breaker can safely share a Redis instance (and database) with other
	// tenants that may already be using the same cluster.  Anything
	// prefixed with `llm:cb:` is owned by the llm-proxy circuit breaker
	// and may be wiped by operators at any time.
	//
	// Per-key state machines (failures/state/halfopen/probe) accept the
	// opaque `key` produced by Transport.keyFor — typically
	// `<provider>:<model>` (or just `<provider>` when the model cannot be
	// extracted).  Embedded colons in the key are concatenated as-is;
	// Redis treats the whole resulting string as one opaque identifier so
	// no extra escaping is needed.
	redisKeyFailures = "llm:cb:failures:" // sorted set of failure timestamps
	redisKeyState    = "llm:cb:state:"    // string: "open" (TTL = cooldown); absence falls back to halfopen marker
	redisKeyHalfOpen = "llm:cb:halfopen:" // marker: while present (and state is absent) the circuit is half-open
	redisKeyProbe    = "llm:cb:probe:"    // short-lived lock for half-open probe slot

	// redisKeyRollup namespaces the per-provider rollup sliding window
	// of `per-key just opened` events.  Keyed by provider name (NOT the
	// per-key keyspace) so a single sorted set captures every model's
	// open events for that provider — that's the whole point of the
	// rollup signal.
	redisKeyRollup = "llm:cb:rollup:"

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
	cfg        Config
	rdb        *redis.Client
	log        *slog.Logger
	failureSeq atomic.Uint64

	// closeOnce / stopAll let Store.Close stop every in-flight
	// KeepProbeAlive goroutine. Without this a leaked stop function (or
	// a panic in a request handler before defer ran) would let the
	// refresh goroutine outlive the process intent and keep extending
	// the Redis TTL forever — which is exactly the wedged-half-open
	// state the circuit breaker is supposed to recover from.
	closeOnce sync.Once
	stopAll   chan struct{}
}

const redisStorePingTimeout = 2 * time.Second

// NewRedisStore constructs a RedisStore connected to the address in cfg.
//
// Connection inputs are resolved in this order:
//  1. If cfg.RedisURL is non-empty it is parsed via redis.ParseURL and its
//     resulting Options form the baseline.
//  2. Otherwise cfg.RedisAddress seeds the baseline Addr.
//  3. The individual RedisPassword / RedisDB fields, when set, overlay
//     whatever the URL parsed.  This lets operators share an existing
//     Redis URL (e.g. from a secret manager) while pinning a dedicated
//     DB to keep `llm:cb:*` keys isolated.
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
	return &RedisStore{
		cfg:     cfg,
		rdb:     client,
		log:     slog.Default(),
		stopAll: make(chan struct{}),
	}, nil
}

// Ping issues a bounded Redis PING to verify connectivity.  Intended for
// one-shot startup health checks; callers should treat a non-nil error as
// "Redis is currently unreachable" rather than fatal — the Store itself is
// designed to fail-open on subsequent errors.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.rdb.Ping(ctx).Err()
}

// Close releases the underlying Redis client's pooled connections and
// signals every outstanding KeepProbeAlive goroutine to exit.  Safe to
// call multiple times; go-redis returns nil on subsequent calls.
func (s *RedisStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.stopAll != nil {
			close(s.stopAll)
		}
	})
	if s.rdb == nil {
		return nil
	}
	return s.rdb.Close()
}

// failuresKey returns the sorted-set key for a key's failure timestamps.
func (s *RedisStore) failuresKey(key string) string {
	return redisKeyFailures + key
}

// stateKey returns the state string key for a circuit key.
func (s *RedisStore) stateKey(key string) string {
	return redisKeyState + key
}

// halfOpenKey returns the half-open marker key for a circuit key.
func (s *RedisStore) halfOpenKey(key string) string {
	return redisKeyHalfOpen + key
}

// probeKey returns the probe-lock key for a circuit key.
func (s *RedisStore) probeKey(key string) string {
	return redisKeyProbe + key
}

// rollupKey returns the sorted-set key for a provider's rollup window.
// The rollup is intentionally provider-scoped, not key-scoped — that's
// what lets it detect "many distinct models are unhappy at once".
func (s *RedisStore) rollupKey(provider string) string {
	return redisKeyRollup + provider
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
func (s *RedisStore) GetState(ctx context.Context, key string) (State, error) {
	val, err := s.rdb.Get(ctx, s.stateKey(key)).Result()
	if err != nil && err != redis.Nil {
		// On Redis errors, fail open (treat as closed) to avoid taking down the
		// service because of a Redis blip.
		s.log.Warn("circuit.RedisStore: GetState failed open",
			"key", key,
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
	exists, hoErr := s.rdb.Exists(ctx, s.halfOpenKey(key)).Result()
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
// ARGV[7] = unique sorted-set member for this failure
//
// Returns: { newState string, openedNow int (0|1) }.  openedNow is 1 only
// when this script invocation is the one that flipped the breaker
// Closed → Open; callers use it to trigger at-most-once side effects (e.g.
// recording a per-provider rollup event) without needing CAS.
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
local member = ARGV[7]

-- Add the new failure and prune the window.  We keep the sorted set alive
-- for at least one full window so the failure count survives even if no
-- further failures arrive before the cooldown ends.
redis.call('ZADD', fkey, now, member)
redis.call('ZREMRANGEBYSCORE', fkey, '-inf', cutoff)
local fttl = cd * 2
if win > fttl then fttl = win end
redis.call('EXPIRE', fkey, math.ceil(fttl))

local count = redis.call('ZCARD', fkey)

local cur = redis.call('GET', skey)
if cur == false then
	local ho = redis.call('EXISTS', hkey)
	if ho == 1 then
		cur = 'half_open'
	else
		cur = 'closed'
	end
end

if tonumber(count) >= thresh and cur == 'closed' then
	redis.call('SET', skey, 'open', 'EX', cd)
	redis.call('SET', hkey, '1', 'EX', hoTTL)
	return {'open', 1}
end

return {cur, 0}
`)

// RecordTerminalFailure records one failure and returns the new state plus
// whether THIS call is what just opened the breaker.
func (s *RedisStore) RecordTerminalFailure(ctx context.Context, key string) (State, bool, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(s.cfg.WindowSeconds)
	member := fmt.Sprintf("%.9f:%d", now, s.failureSeq.Add(1))

	res, err := luaRecordFailure.Run(ctx, s.rdb,
		[]string{s.failuresKey(key), s.stateKey(key), s.halfOpenKey(key)},
		now, cutoff,
		s.cfg.FailureThreshold,
		s.cfg.CooldownSeconds,
		s.cfg.WindowSeconds,
		s.cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier,
		member,
	).Slice()
	if err != nil {
		// On Redis error fail safe: return closed so we don't block traffic.
		s.log.Warn("circuit.RedisStore: luaRecordFailure failed open",
			"key", key,
			"script", "luaRecordFailure",
			"redis_client", fmt.Sprintf("%p", s.rdb),
			"error", err,
			"ctx_err", ctx.Err(),
		)
		return StateClosed, false, nil
	}
	if len(res) < 2 {
		s.log.Warn("circuit.RedisStore: luaRecordFailure unexpected reply shape",
			"key", key,
			"reply_len", len(res),
		)
		return StateClosed, false, nil
	}
	stateStr, _ := res[0].(string)
	// Lua integers come back to go-redis as int64; defensively coerce.
	openedNow := false
	switch v := res[1].(type) {
	case int64:
		openedNow = v == 1
	case string:
		openedNow = v == "1"
	}
	switch stateStr {
	case "open":
		return StateOpen, openedNow, nil
	case "half_open":
		return StateHalfOpen, openedNow, nil
	default:
		return StateClosed, openedNow, nil
	}
}

// RecordSuccess closes the circuit and removes all failure history.
func (s *RedisStore) RecordSuccess(ctx context.Context, key string) error {
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, s.stateKey(key))
	pipe.Del(ctx, s.halfOpenKey(key))
	pipe.Del(ctx, s.failuresKey(key))
	pipe.Del(ctx, s.probeKey(key))
	_, err := pipe.Exec(ctx)
	return err
}

// RecordProbeFailed re-opens the circuit for another full cooldown.
func (s *RedisStore) RecordProbeFailed(ctx context.Context, key string) error {
	cd := time.Duration(s.cfg.CooldownSeconds) * time.Second
	hoTTL := time.Duration(s.cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier) * time.Second
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.stateKey(key), "open", cd)
	pipe.Set(ctx, s.halfOpenKey(key), "1", hoTTL)
	pipe.Del(ctx, s.probeKey(key))
	_, err := pipe.Exec(ctx)
	return err
}

// GetStats returns a snapshot of circuit stats for the key.
func (s *RedisStore) GetStats(ctx context.Context, key string) (*ProviderStats, error) {
	state, err := s.GetState(ctx, key)
	if err != nil {
		return nil, err
	}
	stats := &ProviderStats{State: state}

	// Count failures in the current window.
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(s.cfg.WindowSeconds)
	count, err := s.rdb.ZCount(ctx, s.failuresKey(key),
		fmt.Sprintf("%f", cutoff), "+inf").Result()
	if err == nil {
		stats.Failures = int(count)
	}

	// If open, try to surface the cooldown expiry from the key TTL.
	if state == StateOpen {
		dur, err := s.rdb.TTL(ctx, s.stateKey(key)).Result()
		if err == nil && dur > 0 {
			t := time.Now().Add(dur)
			stats.CooldownUntil = &t
		}
	}

	return stats, nil
}

// RecordKeyOpenedForRollup appends a (timestamp, openedKey) entry to the
// per-provider rollup sliding window and prunes anything outside the
// configured window.  ZADD with member=openedKey naturally dedupes
// concurrent records of the same model opening "at the same moment"
// across instances; subsequent re-opens of the same key after recovery
// overwrite the score with a fresh timestamp, which is the desired
// behaviour (a flapping key continues to count toward the rollup).
func (s *RedisStore) RecordKeyOpenedForRollup(ctx context.Context, provider string, openedKey string, windowSeconds int) error {
	if windowSeconds <= 0 {
		return nil
	}
	rkey := s.rollupKey(provider)
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(windowSeconds)

	pipe := s.rdb.Pipeline()
	pipe.ZAdd(ctx, rkey, redis.Z{Score: now, Member: openedKey})
	pipe.ZRemRangeByScore(ctx, rkey, "-inf", fmt.Sprintf("%f", cutoff))
	// TTL the rollup set generously so a long quiet period auto-cleans
	// without an explicit reaper; doubled for safety against clock skew.
	pipe.Expire(ctx, rkey, time.Duration(windowSeconds*2)*time.Second)
	_, err := pipe.Exec(ctx)
	if err != nil {
		s.log.Warn("circuit.RedisStore: RecordKeyOpenedForRollup failed (rollup signal will lag)",
			"provider", provider,
			"key", openedKey,
			"error", err,
		)
		// Fail-open: a missed rollup record cannot wedge the proxy; the
		// per-key breaker still protects callers.  Mirror the rest of
		// the Store's fail-open posture so a Redis blip never escalates.
		return nil
	}
	return nil
}

// RollupOpen reports whether the per-provider rollup window holds at least
// `threshold` distinct key-open events.  Returns (false, 0, nil) when
// threshold is zero (rollup feature disabled), and on Redis errors —
// failing open keeps the proxy serving traffic when the rollup signal
// itself is unavailable.
func (s *RedisStore) RollupOpen(ctx context.Context, provider string, threshold, windowSeconds int) (bool, int, error) {
	if threshold <= 0 || windowSeconds <= 0 {
		return false, 0, nil
	}
	rkey := s.rollupKey(provider)
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(windowSeconds)
	count, err := s.rdb.ZCount(ctx, rkey,
		fmt.Sprintf("%f", cutoff), "+inf").Result()
	if err != nil {
		s.log.Warn("circuit.RedisStore: RollupOpen failed open",
			"provider", provider,
			"error", err,
			"ctx_err", ctx.Err(),
		)
		return false, 0, nil
	}
	return int(count) >= threshold, int(count), nil
}

// ClearRollupKey removes openedKey from the per-provider rollup sorted
// set so the rollup signal tracks currently-degraded models rather than
// just historical opens.  Idempotent and fail-open on Redis errors.
func (s *RedisStore) ClearRollupKey(ctx context.Context, provider string, openedKey string) error {
	if openedKey == "" {
		return nil
	}
	if err := s.rdb.ZRem(ctx, s.rollupKey(provider), openedKey).Err(); err != nil {
		s.log.Warn("circuit.RedisStore: ClearRollupKey failed (rollup may lag)",
			"provider", provider,
			"key", openedKey,
			"error", err,
		)
	}
	return nil
}

// RolledUpKeys returns the set of keys currently inside the rollup
// window for `provider`.  Aged-out members are excluded by score-based
// range query, so the answer reflects the live window without first
// requiring a separate prune.  Order is timestamp-ascending.  Fails
// open (returns nil, nil) on Redis errors so /health stays responsive
// when the cache backend is unavailable.
func (s *RedisStore) RolledUpKeys(ctx context.Context, provider string, windowSeconds int) ([]string, error) {
	if windowSeconds <= 0 {
		return nil, nil
	}
	rkey := s.rollupKey(provider)
	cutoff := float64(time.Now().UnixNano())/1e9 - float64(windowSeconds)
	members, err := s.rdb.ZRangeByScore(ctx, rkey, &redis.ZRangeBy{
		Min: fmt.Sprintf("%f", cutoff),
		Max: "+inf",
	}).Result()
	if err != nil {
		s.log.Warn("circuit.RedisStore: RolledUpKeys failed (rollup view will lag)",
			"provider", provider,
			"error", err,
		)
		return nil, nil
	}
	return members, nil
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

// luaTryStartProbe atomically gates probe-slot acquisition on the circuit
// being in the HalfOpen state. Without this gate a stale probe key could
// otherwise be acquired during Open (stateKey present → fast-fail expected)
// or Closed (halfOpen marker absent → no probe needed). The pure-Go SetNX
// version previously here matched MemoryStore.TryStartProbe's signature
// but skipped the StateHalfOpen check that MemoryStore enforces.
var luaTryStartProbe = redis.NewScript(`
local stateKey = KEYS[1]
local halfOpenKey = KEYS[2]
local probeKey = KEYS[3]
local ttl = tonumber(ARGV[1])
if redis.call('EXISTS', stateKey) == 1 then
  return 0
end
if redis.call('EXISTS', halfOpenKey) == 0 then
  return 0
end
if redis.call('SET', probeKey, '1', 'NX', 'EX', ttl) then
  return 1
end
return 0
`)

// TryStartProbe atomically acquires the half-open probe slot, but only when
// the circuit is genuinely in HalfOpen (stateKey absent + halfOpenKey present).
// Returns true only if this call won the race AND the circuit was eligible
// for a probe. Memory parity: MemoryStore.TryStartProbe explicitly checks
// stateUnlocked(e) != StateHalfOpen.
func (s *RedisStore) TryStartProbe(ctx context.Context, key string) bool {
	res, err := luaTryStartProbe.Run(ctx, s.rdb,
		[]string{s.stateKey(key), s.halfOpenKey(key), s.probeKey(key)},
		int(probeLockTTL.Seconds()),
	).Int64()
	if err != nil {
		return false
	}
	return res == 1
}

// ReleaseProbe drops the probe slot without changing the circuit state.
// This is the right call when a probe did not produce a signal we want to
// credit to the upstream (e.g. the caller's context was cancelled or its
// deadline expired before the upstream responded), because in that case
// the outcome tells us nothing about provider health.  Using SetNX + DEL
// here matches the semantics of RecordSuccess / RecordProbeFailed, which
// both also clear the probe key.
func (s *RedisStore) ReleaseProbe(ctx context.Context, key string) error {
	return s.rdb.Del(ctx, s.probeKey(key)).Err()
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
func (s *RedisStore) KeepProbeAlive(key string) (stop func()) {
	done := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(probeLockRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.stopAll:
				// Store-level shutdown: stop refreshing immediately so
				// the goroutine does not outlive Close().
				return
			case <-ticker.C:
				// Re-assert the TTL.  We use a bounded context rather
				// than Background so the refresh op itself can't hang
				// behind a Redis failure.
				refreshCtx, cancel := context.WithTimeout(context.Background(), probeLockTTL)
				_ = s.rdb.Expire(refreshCtx, s.probeKey(key), probeLockTTL).Err()
				cancel()
			}
		}
	}()

	return func() { once.Do(func() { close(done) }) }
}
