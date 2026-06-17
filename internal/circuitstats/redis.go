package circuitstats

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	redisCountersKey = "llm:cb:activity:counters"
	redisEventsKey   = "llm:cb:activity:events"
	redisProviderPFX = "provider:"

	redisOpTimeout = 500 * time.Millisecond
)

// recordEventScript appends a JSON event and trims the list. Counter totals
// live in admin rollups; this script keeps legacy counter fields best-effort
// for operators inspecting the circuit-breaker Redis DB directly.
var recordEventScript = redis.NewScript(`
local counters = KEYS[1]
local events = KEYS[2]
local counter_field = ARGV[1]
local provider = ARGV[2]
local event_json = ARGV[3]
local max_events = tonumber(ARGV[4])
local started_at = ARGV[5]

redis.call('HINCRBY', counters, counter_field, 1)
if provider ~= '' then
  redis.call('HINCRBY', counters, '` + redisProviderPFX + `' .. provider, 1)
end
redis.call('LPUSH', events, event_json)
redis.call('LTRIM', events, 0, max_events - 1)
redis.call('HSETNX', counters, 'started_at', started_at)
return 1
`)

// NewRedisRecorder returns a recorder that mirrors recent events across tasks
// via Redis (same DB as the circuit breaker). The client is not closed by the
// recorder — the circuit store owns it.
func NewRedisRecorder(client *redis.Client, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{
		startedAt:  time.Now().UTC(),
		dayKey:     time.Now().UTC().Format("2006-01-02"),
		byProvider: make(map[string]int64),
		byKey:      make(map[string]int64),
		rdb:        client,
		log:        log,
	}
}

func (r *Recorder) redisEnabled() bool {
	return r != nil && r.rdb != nil
}

func (r *Recorder) recordRedisEvent(counterField, provider string, e activityEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()

	payload, err := json.Marshal(e)
	if err != nil {
		return
	}

	err = recordEventScript.Run(
		ctx,
		r.rdb,
		[]string{redisCountersKey, redisEventsKey},
		counterField,
		provider,
		string(payload),
		strconv.Itoa(MaxRecentEvents),
		strconv.FormatInt(time.Now().UTC().Unix(), 10),
	).Err()
	if err != nil && r.log != nil {
		r.log.Warn("circuitstats: redis event write failed", "field", counterField, "error", err)
	}
}

func (r *Recorder) incrRedisCheckAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
		defer cancel()
		pipe := r.rdb.Pipeline()
		pipe.HIncrBy(ctx, redisCountersKey, "checks_total", 1)
		pipe.HSetNX(ctx, redisCountersKey, "started_at", strconv.FormatInt(time.Now().UTC().Unix(), 10))
		if _, err := pipe.Exec(ctx); err != nil && r.log != nil {
			r.log.Warn("circuitstats: redis check increment failed", "error", err)
		}
	}()
}

func (r *Recorder) mergeRedisRecentEvents(snap map[string]interface{}) {
	if !r.redisEnabled() || snap == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()

	rawEvents, err := r.rdb.LRange(ctx, redisEventsKey, 0, MaxRecentEvents-1).Result()
	if err != nil {
		if r.log != nil {
			r.log.Warn("circuitstats: redis recent events read failed", "error", err)
		}
		return
	}
	if len(rawEvents) == 0 {
		return
	}

	recent := make([]activityEvent, 0, len(rawEvents))
	for _, raw := range rawEvents {
		var e activityEvent
		if json.Unmarshal([]byte(raw), &e) == nil {
			recent = append(recent, e)
		}
	}
	snap["recent_events"] = recent
}
