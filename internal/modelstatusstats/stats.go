package modelstatusstats

import (
	"context"
	"fmt"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const redisOpTimeout = 500 * time.Millisecond
const redisKeyTTL = 48 * time.Hour

// Snapshot holds in-process counters for retired and deprecated model calls.
type Snapshot struct {
	Retired    map[string]int64 `json:"retired"`
	Deprecated map[string]int64 `json:"deprecated"`
}

// Recorder accumulates retired and deprecated model call counts in-process,
// with optional Redis mirroring for fleet-wide visibility.
type Recorder struct {
	mu         sync.RWMutex
	retired    map[string]int64
	deprecated map[string]int64
	rdb        *redis.Client
}

// NewRecorder returns a recorder. The Redis client is optional; when nil only
// in-process counters are updated.
func NewRecorder(rdb *redis.Client) *Recorder {
	return &Recorder{
		retired:    make(map[string]int64),
		deprecated: make(map[string]int64),
		rdb:        rdb,
	}
}

func composeKey(provider, model string) string {
	return provider + ":" + model
}

func redisKey(kind, provider, model string) string {
	day := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("llm:model:%s:day:%s:%s:%s", kind, day, provider, model)
}

func (r *Recorder) incrRedis(kind, provider, model string) {
	if r == nil || r.rdb == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
		defer cancel()
		key := redisKey(kind, provider, model)
		pipe := r.rdb.Pipeline()
		pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, redisKeyTTL)
		_, _ = pipe.Exec(ctx)
	}()
}

func (r *Recorder) bumpLocked(counter map[string]int64, provider, model string) {
	if provider == "" || model == "" {
		return
	}
	counter[composeKey(provider, model)]++
}

// RecordRetired increments the retired-model counter.
func (r *Recorder) RecordRetired(provider, model string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.bumpLocked(r.retired, provider, model)
	r.mu.Unlock()
	r.incrRedis("retired", provider, model)
}

// RecordDeprecated increments the deprecated-model counter.
func (r *Recorder) RecordDeprecated(provider, model string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.bumpLocked(r.deprecated, provider, model)
	r.mu.Unlock()
	r.incrRedis("deprecated", provider, model)
}

// Snapshot returns a copy of current in-process counters.
func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{
			Retired:    map[string]int64{},
			Deprecated: map[string]int64{},
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	retired := make(map[string]int64, len(r.retired))
	for k, v := range r.retired {
		retired[k] = v
	}
	deprecated := make(map[string]int64, len(r.deprecated))
	for k, v := range r.deprecated {
		deprecated[k] = v
	}
	return Snapshot{
		Retired:    retired,
		Deprecated: deprecated,
	}
}
