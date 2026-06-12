package ratelimit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	redis "github.com/redis/go-redis/v9"
)

// redisLimiter is a Redis-backed rate limiter mirroring memoryLimiter behavior.
type redisLimiter struct {
	cfg          *config.YAMLConfig
	rdb          *redis.Client
	perKeyLookup PerKeyOverrideFunc
}

// SetPerKeyOverride installs a dynamic per-key override source (e.g. backed by
// the API-key store). Safe to call once at startup before serving traffic.
func (r *redisLimiter) SetPerKeyOverride(fn PerKeyOverrideFunc) {
	r.perKeyLookup = fn
}

// NewRedisLimiter constructs a Redis-backed rate limiter.
//
// Connection options are derived from RedisConfig.URL (preferred — supports
// rediss:// for TLS and embeds auth + DB) falling back to the discrete
// Address/Password/DB triple. An explicit DBSet override always wins so a
// shared Redis instance can pin a specific DB regardless of the URL.
//
// The constructor performs a Ping with a 5s context so a misconfigured
// Redis at process startup fails fast instead of producing per-request
// errors once traffic arrives.
func NewRedisLimiter(cfg *config.YAMLConfig) (RateLimiter, error) {
	if cfg == nil || cfg.Features.RateLimiting.Redis == nil {
		return nil, fmt.Errorf("redis configuration is required")
	}
	r := cfg.Features.RateLimiting.Redis

	// Expand `${VAR}` / `$VAR` tokens against the process environment so
	// deployers can wire secrets (e.g. `REDIS_URL` rendered by a secret
	// manager) into YAML without baking credentials into the file. Mirrors
	// the same expansion the circuit breaker does in cmd/llm-proxy/main.go;
	// without it, `url: ${REDIS_URL}` was being passed verbatim to
	// redis.ParseURL and the limiter would fail to construct.
	url := os.ExpandEnv(r.URL)
	addr := os.ExpandEnv(r.Address)
	password := os.ExpandEnv(r.Password)

	var opts *redis.Options
	if url != "" {
		parsed, err := redis.ParseURL(url)
		if err != nil {
			return nil, fmt.Errorf("invalid redis URL: %w", err)
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
		opts = &redis.Options{Addr: addr, Password: password, DB: r.DB}
	}

	client := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return &redisLimiter{cfg: cfg, rdb: client}, nil
}

func (r *redisLimiter) scopeKeys(scope ScopeKeys) []string {
	keys := []string{"global"}
	if scope.Provider != "" {
		keys = append(keys, "provider:"+scope.Provider)
	}
	if scope.Model != "" {
		keys = append(keys, "model:"+scope.Model)
	}
	if scope.APIKey != "" {
		keys = append(keys, "key:"+scope.APIKey)
	}
	if scope.UserID != "" {
		keys = append(keys, "user:"+scope.UserID)
	}
	return keys
}

type rlLimits struct {
	reqPerWindow int
	tokPerWindow int
}

func (r *redisLimiter) limitFor(key string, minute bool) rlLimits {
	base := r.cfg.Features.RateLimiting.Limits
	lim := rlLimits{}
	if minute {
		lim.reqPerWindow = base.RequestsPerMinute
		lim.tokPerWindow = base.TokensPerMinute
	} else {
		lim.reqPerWindow = base.RequestsPerDay
		lim.tokPerWindow = base.TokensPerDay
	}

	overrides := r.cfg.Features.RateLimiting.Overrides
	if strings.HasPrefix(key, "model:") {
		name := strings.TrimPrefix(key, "model:")
		if o, ok := overrides.PerModel[name]; ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	} else if strings.HasPrefix(key, "key:") {
		id := strings.TrimPrefix(key, "key:")
		o, ok := overrides.PerKey[id]
		// Dynamic per-key overrides (API-key record) take precedence over
		// static YAML overrides when present.
		if r.perKeyLookup != nil {
			if dyn, found := r.perKeyLookup(id); found {
				o, ok = dyn, true
			}
		}
		if ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	} else if strings.HasPrefix(key, "user:") {
		id := strings.TrimPrefix(key, "user:")
		if o, ok := overrides.PerUser[id]; ok {
			if minute {
				if o.RequestsPerMinute > 0 {
					lim.reqPerWindow = o.RequestsPerMinute
				}
				if o.TokensPerMinute > 0 {
					lim.tokPerWindow = o.TokensPerMinute
				}
			} else {
				if o.RequestsPerDay > 0 {
					lim.reqPerWindow = o.RequestsPerDay
				}
				if o.TokensPerDay > 0 {
					lim.tokPerWindow = o.TokensPerDay
				}
			}
		}
	}
	return lim
}

func minuteKey(scopeKey string) string { return "rl:min:" + scopeKey }
func dayKey(scopeKey string) string    { return "rl:day:" + scopeKey }

func secToMinuteEnd(t time.Time) int {
	s := int(t.Unix() % 60)
	if s == 0 {
		return 60
	}
	return 60 - s
}

func secToDayEnd(t time.Time) int {
	// Use the UTC calendar day so the rate-limit "day" window aligns with the
	// admin rollups and the in-memory limiter (which truncate on UTC). Using
	// the local TZ here meant the daily window and its TTL could roll at a
	// different instant than the rest of the system if the process TZ drifted
	// from UTC.
	t = t.UTC()
	y, m, d := t.Date()
	end := time.Date(y, m, d, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC)
	return int(end.Sub(t).Seconds()) + 1
}

var luaCheckAndReserve = redis.NewScript(`
local est = tonumber(ARGV[1])
local ttlMin = tonumber(ARGV[2])
local ttlDay = tonumber(ARGV[3])
local pairsCount = tonumber(ARGV[4])
local limitsBase = 5
for i = 0, pairsCount - 1 do
  local kMin = KEYS[2*i + 1]
  local kDay = KEYS[2*i + 2]
  local minReq = tonumber(redis.call('HGET', kMin, 'req') or '0')
  local minTok = tonumber(redis.call('HGET', kMin, 'tok') or '0')
  local dayReq = tonumber(redis.call('HGET', kDay, 'req') or '0')
  local dayTok = tonumber(redis.call('HGET', kDay, 'tok') or '0')
  local limMinReq = tonumber(ARGV[limitsBase + 4*i])
  local limMinTok = tonumber(ARGV[limitsBase + 4*i + 1])
  local limDayReq = tonumber(ARGV[limitsBase + 4*i + 2])
  local limDayTok = tonumber(ARGV[limitsBase + 4*i + 3])
  if limMinReq > 0 and (minReq + 1 > limMinReq) then
    local rem = limMinReq - (minReq + 1)
    if rem < 0 then rem = 0 end
    return {0, 'minute', 'requests', limMinReq, rem, i, ttlMin}
  end
  if limMinTok > 0 then
    if not (minTok == 0) then
      if (minTok + est > limMinTok) then
        local rem = limMinTok - (minTok + est)
        if rem < 0 then rem = 0 end
        return {0, 'minute', 'tokens', limMinTok, rem, i, ttlMin}
      end
    end
  end
  if limDayReq > 0 and (dayReq + 1 > limDayReq) then
    local rem = limDayReq - (dayReq + 1)
    if rem < 0 then rem = 0 end
    return {0, 'day', 'requests', limDayReq, rem, i, ttlDay}
  end
  if limDayTok > 0 then
    if not (dayTok == 0) then
      if (dayTok + est > limDayTok) then
        local rem = limDayTok - (dayTok + est)
        if rem < 0 then rem = 0 end
        return {0, 'day', 'tokens', limDayTok, rem, i, ttlDay}
      end
    end
  end
end
for i = 0, pairsCount - 1 do
  local kMin = KEYS[2*i + 1]
  local kDay = KEYS[2*i + 2]
  redis.call('HINCRBY', kMin, 'req', 1)
  redis.call('HINCRBY', kMin, 'tok', est)
  redis.call('EXPIRE', kMin, ttlMin)
  redis.call('HINCRBY', kDay, 'req', 1)
  redis.call('HINCRBY', kDay, 'tok', est)
  redis.call('EXPIRE', kDay, ttlDay)
end
return {1}
`)

var luaAdjust = redis.NewScript(`
local delta = tonumber(ARGV[1])
local ttlMin = tonumber(ARGV[2])
local ttlDay = tonumber(ARGV[3])
local pairsCount = tonumber(ARGV[4])
for i = 0, pairsCount - 1 do
  local kMin = KEYS[2*i + 1]
  local kDay = KEYS[2*i + 2]
  local newMin = redis.call('HINCRBY', kMin, 'tok', delta)
  if tonumber(newMin) < 0 then redis.call('HSET', kMin, 'tok', 0) end
  redis.call('EXPIRE', kMin, ttlMin)
  local newDay = redis.call('HINCRBY', kDay, 'tok', delta)
  if tonumber(newDay) < 0 then redis.call('HSET', kDay, 'tok', 0) end
  redis.call('EXPIRE', kDay, ttlDay)
end
return {1}
`)

// luaCancel undoes a prior reservation. estTokens MUST mirror what was
// passed to luaCheckAndReserve; otherwise the token reservation lingers
// in the per-window hash and silently under-credits the limit window.
var luaCancel = redis.NewScript(`
local estTokens = tonumber(ARGV[1])
local ttlMin = tonumber(ARGV[2])
local ttlDay = tonumber(ARGV[3])
local pairsCount = tonumber(ARGV[4])
for i = 0, pairsCount - 1 do
  local kMin = KEYS[2*i + 1]
  local kDay = KEYS[2*i + 2]
  local newMinReq = redis.call('HINCRBY', kMin, 'req', -1)
  if tonumber(newMinReq) < 0 then redis.call('HSET', kMin, 'req', 0) end
  local newMinTok = redis.call('HINCRBY', kMin, 'tok', -estTokens)
  if tonumber(newMinTok) < 0 then redis.call('HSET', kMin, 'tok', 0) end
  redis.call('EXPIRE', kMin, ttlMin)
  local newDayReq = redis.call('HINCRBY', kDay, 'req', -1)
  if tonumber(newDayReq) < 0 then redis.call('HSET', kDay, 'req', 0) end
  local newDayTok = redis.call('HINCRBY', kDay, 'tok', -estTokens)
  if tonumber(newDayTok) < 0 then redis.call('HSET', kDay, 'tok', 0) end
  redis.call('EXPIRE', kDay, ttlDay)
end
return {1}
`)

// snapshotTimeout bounds the Redis SCAN+HGETALL done for the admin dashboard
// so a slow/large keyspace can never stall the admin API request.
const snapshotTimeout = 2 * time.Second

// Snapshot implements Snapshotter for the Redis backend so the admin dashboard
// shows live fleet-wide counters (not just configured limits). It SCANs the
// rl:min:* and rl:day:* hashes on the rate-limit DB and reports each scope's
// current req/tok. Best-effort: on any Redis error it returns the configured
// limits with empty counters rather than failing the admin request.
func (r *redisLimiter) Snapshot(now time.Time) LimitsSnapshot {
	rl := r.cfg.Features.RateLimiting
	snap := LimitsSnapshot{
		Enabled:   rl.Enabled,
		Backend:   "redis",
		Limits:    rl.Limits,
		Overrides: rl.Overrides,
		Minute: &WindowSnapshot{
			WindowStart: now.UTC().Truncate(time.Minute).Format(time.RFC3339),
			Counters:    map[string]CounterSnapshot{},
		},
		Day: &WindowSnapshot{
			WindowStart: now.UTC().Truncate(24 * time.Hour).Format(time.RFC3339),
			Counters:    map[string]CounterSnapshot{},
		},
	}
	if r.rdb == nil {
		return snap
	}
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	r.scanCounters(ctx, "rl:min:", snap.Minute.Counters)
	r.scanCounters(ctx, "rl:day:", snap.Day.Counters)
	return snap
}

// scanCounters loads every "<prefix><scope>" hash into out keyed by the bare
// scope string (matching the memory limiter's counter keys, e.g. "global",
// "user:alice"). Errors abort the scan and leave out partially populated.
func (r *redisLimiter) scanCounters(ctx context.Context, prefix string, out map[string]CounterSnapshot) {
	var cursor uint64
	for {
		keys, next, err := r.rdb.Scan(ctx, cursor, prefix+"*", 200).Result()
		if err != nil {
			return
		}
		for _, k := range keys {
			h, err := r.rdb.HGetAll(ctx, k).Result()
			if err != nil {
				return
			}
			req, _ := strconv.Atoi(h["req"])
			tok, _ := strconv.Atoi(h["tok"])
			out[strings.TrimPrefix(k, prefix)] = CounterSnapshot{Requests: req, Tokens: tok}
		}
		cursor = next
		if cursor == 0 {
			return
		}
	}
}

func (r *redisLimiter) CheckAndReserve(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) (ReservationResult, error) {
	scopeKeys := r.scopeKeys(scope)
	keys := make([]string, 0, len(scopeKeys)*2)
	argLimits := make([]interface{}, 0, len(scopeKeys)*4)
	for _, sk := range scopeKeys {
		keys = append(keys, minuteKey(sk))
		keys = append(keys, dayKey(sk))
		minLim := r.limitFor(sk, true)
		dayLim := r.limitFor(sk, false)
		argLimits = append(argLimits, minLim.reqPerWindow, minLim.tokPerWindow, dayLim.reqPerWindow, dayLim.tokPerWindow)
	}
	argv := make([]interface{}, 0, 4+len(argLimits))
	argv = append(argv, estTokens)
	argv = append(argv, secToMinuteEnd(now))
	argv = append(argv, secToDayEnd(now))
	argv = append(argv, len(scopeKeys))
	argv = append(argv, argLimits...)
	res, err := luaCheckAndReserve.Run(ctx, r.rdb, keys, argv...).Result()
	if err != nil {
		return ReservationResult{}, err
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return ReservationResult{}, fmt.Errorf("invalid redis script result")
	}
	okFlag, _ := arr[0].(int64)
	if okFlag == 1 {
		return ReservationResult{Allowed: true, ReservationID: id}, nil
	}
	window, _ := arr[1].(string)
	metric, _ := arr[2].(string)
	limit := toInt(arr[3])
	remaining := toInt(arr[4])
	idx := toInt(arr[5])
	retry := toInt(arr[6])
	scopeKey := ""
	if idx >= 0 && idx < len(scopeKeys) {
		scopeKey = scopeKeys[idx]
	}
	details := &LimitDetails{ScopeKey: scopeKey, Metric: metric, Window: window, Limit: limit, Remaining: remaining}
	reason := window + " limit exceeded"
	return ReservationResult{Allowed: false, RetryAfterSeconds: retry, Reason: reason, Details: details}, nil
}

func (r *redisLimiter) Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, now time.Time) error {
	_ = id
	scopeKeys := r.scopeKeys(scope)
	if len(scopeKeys) == 0 {
		return nil
	}
	keys := make([]string, 0, len(scopeKeys)*2)
	for _, sk := range scopeKeys {
		keys = append(keys, minuteKey(sk))
		keys = append(keys, dayKey(sk))
	}
	argv := []interface{}{tokenDelta, secToMinuteEnd(now), secToDayEnd(now), len(scopeKeys)}
	_, err := luaAdjust.Run(ctx, r.rdb, keys, argv...).Result()
	return err
}

func (r *redisLimiter) Cancel(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) error {
	_ = id
	scopeKeys := r.scopeKeys(scope)
	if len(scopeKeys) == 0 {
		return nil
	}
	keys := make([]string, 0, len(scopeKeys)*2)
	for _, sk := range scopeKeys {
		keys = append(keys, minuteKey(sk))
		keys = append(keys, dayKey(sk))
	}
	argv := []interface{}{estTokens, secToMinuteEnd(now), secToDayEnd(now), len(scopeKeys)}
	_, err := luaCancel.Run(ctx, r.rdb, keys, argv...).Result()
	return err
}

// Close releases the Redis client. Returns ErrClosed if already closed.
func (r *redisLimiter) Close() error {
	if r.rdb == nil {
		return nil
	}
	return r.rdb.Close()
}

func toInt(v interface{}) int {
	switch t := v.(type) {
	case int64:
		return int(t)
	case int:
		return t
	case string:
		var n int
		_, _ = fmt.Sscanf(t, "%d", &n)
		return n
	default:
		return 0
	}
}
