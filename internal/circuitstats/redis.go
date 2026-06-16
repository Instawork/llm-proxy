package circuitstats

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	redisCountersKey = "llm:cb:activity:counters"
	redisEventsKey   = "llm:cb:activity:events"
	redisProviderPFX = "provider:"

	redisOpTimeout = 500 * time.Millisecond
)

// recordEventScript atomically bumps a counter, optional per-provider tally,
// appends a JSON event, trims the list, and sets started_at once.
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

// NewRedisRecorder returns a recorder that shares activity across tasks via
// Redis (same DB as the circuit breaker). The client is not closed by the
// recorder — the circuit store owns it.
func NewRedisRecorder(client *redis.Client, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{
		startedAt:  time.Now().UTC(),
		byProvider: make(map[string]int64),
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

func (r *Recorder) snapshotRedis() map[string]interface{} {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pipe := r.rdb.Pipeline()
	countersCmd := pipe.HGetAll(ctx, redisCountersKey)
	eventsCmd := pipe.LRange(ctx, redisEventsKey, 0, MaxRecentEvents-1)
	if _, err := pipe.Exec(ctx); err != nil {
		if r.log != nil {
			r.log.Warn("circuitstats: redis snapshot failed", "error", err)
		}
		return r.snapshotMemory()
	}

	counters, err := countersCmd.Result()
	if err != nil {
		return r.snapshotMemory()
	}
	rawEvents, err := eventsCmd.Result()
	if err != nil {
		return r.snapshotMemory()
	}

	byProvider := make(map[string]int64)
	for field, val := range counters {
		if strings.HasPrefix(field, redisProviderPFX) {
			provider := strings.TrimPrefix(field, redisProviderPFX)
			if n, parseErr := strconv.ParseInt(val, 10, 64); parseErr == nil {
				byProvider[provider] = n
			}
		}
	}

	recent := make([]activityEvent, 0, len(rawEvents))
	for _, raw := range rawEvents {
		var e activityEvent
		if json.Unmarshal([]byte(raw), &e) == nil {
			recent = append(recent, e)
		}
	}

	startedAt := parseCounter(counters["started_at"])
	if startedAt == 0 {
		startedAt = r.startedAt.Unix()
	}

	return map[string]interface{}{
		"available":        true,
		"backend":          "redis",
		"started_at":       startedAt,
		"checks_total":     parseCounter(counters["checks_total"]),
		"blocked_open":     parseCounter(counters["blocked_open"]),
		"probes_started":   parseCounter(counters["probes_started"]),
		"probes_succeeded": parseCounter(counters["probes_succeeded"]),
		"probes_failed":    parseCounter(counters["probes_failed"]),
		"circuits_opened":  parseCounter(counters["circuits_opened"]),
		"by_provider":      byProvider,
		"recent_events":    recent,
	}
}

func parseCounter(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
