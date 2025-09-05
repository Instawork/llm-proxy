package ratelimit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	redis "github.com/redis/go-redis/v9"
)

// redisLimiter is a Redis-backed rate limiter mirroring memoryLimiter behavior.
type redisLimiter struct {
	cfg *config.YAMLConfig
	rdb *redis.Client
}

func NewRedisLimiter(cfg *config.YAMLConfig) (RateLimiter, error) {
	if cfg == nil || cfg.Features.RateLimiting.Redis == nil {
		return nil, fmt.Errorf("redis configuration is required")
	}
	r := cfg.Features.RateLimiting.Redis
	client := redis.NewClient(&redis.Options{
		Addr:     r.Address,
		Password: r.Password,
		DB:       r.DB,
	})
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
		if o, ok := overrides.PerKey[id]; ok {
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
	y, m, d := t.Date()
	loc := t.Location()
	end := time.Date(y, m, d, 23, 59, 59, int(time.Second-time.Nanosecond), loc)
	return int(time.Until(end).Seconds()) + 1
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

var luaCancel = redis.NewScript(`
local ttlMin = tonumber(ARGV[1])
local ttlDay = tonumber(ARGV[2])
local pairsCount = tonumber(ARGV[3])
for i = 0, pairsCount - 1 do
  local kMin = KEYS[2*i + 1]
  local kDay = KEYS[2*i + 2]
  local newMinReq = redis.call('HINCRBY', kMin, 'req', -1)
  if tonumber(newMinReq) < 0 then redis.call('HSET', kMin, 'req', 0) end
  local minTok = tonumber(redis.call('HGET', kMin, 'tok') or '0')
  if minTok < 0 then redis.call('HSET', kMin, 'tok', 0) end
  redis.call('EXPIRE', kMin, ttlMin)
  local newDayReq = redis.call('HINCRBY', kDay, 'req', -1)
  if tonumber(newDayReq) < 0 then redis.call('HSET', kDay, 'req', 0) end
  local dayTok = tonumber(redis.call('HGET', kDay, 'tok') or '0')
  if dayTok < 0 then redis.call('HSET', kDay, 'tok', 0) end
  redis.call('EXPIRE', kDay, ttlDay)
end
return {1}
`)

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

func (r *redisLimiter) Cancel(ctx context.Context, id string, scope ScopeKeys, now time.Time) error {
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
	argv := []interface{}{secToMinuteEnd(now), secToDayEnd(now), len(scopeKeys)}
	_, err := luaCancel.Run(ctx, r.rdb, keys, argv...).Result()
	return err
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
