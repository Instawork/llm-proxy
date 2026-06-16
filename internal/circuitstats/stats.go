// Package circuitstats records circuit-breaker activity for the admin dashboard:
// state checks, fast-fails while open, and half-open probe outcomes when a
// cooldown window ends. Use NewRecorder for local dev; NewRedisRecorder shares
// counters and recent events across ECS tasks via the circuit-breaker Redis DB.
package circuitstats

import (
	"log/slog"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"
)

// MaxRecentEvents bounds the recent-activity ring buffer.
const MaxRecentEvents = 50

// Event kinds surfaced in the admin UI.
const (
	EventProbe         = "probe"          // half-open probe sent upstream (cooldown ended)
	EventProbeClosed   = "probe_closed"   // probe succeeded → circuit closed
	EventProbeReopened = "probe_reopened" // probe failed → circuit open again
	EventFastFail      = "fast_fail"      // request blocked while open
	EventOpened        = "opened"         // circuit tripped (threshold or force-open)
)

type activityEvent struct {
	Time        int64  `json:"time"`
	Provider    string `json:"provider"`
	Key         string `json:"key,omitempty"`
	Kind        string `json:"kind"`
	NewState    string `json:"new_state,omitempty"`
	StatusCode  int    `json:"status_code,omitempty"`
	FailureKind string `json:"failure_kind,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// Recorder accumulates rolling circuit activity. Without a Redis client each
// sidecar keeps its own counters; with Redis (NewRedisRecorder) activity is
// shared cluster-wide under llm:cb:activity:* keys.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time

	checksTotal     int64
	blockedOpen     int64
	probesStarted   int64
	probesSucceeded int64
	probesFailed    int64
	circuitsOpened  int64

	byProvider map[string]int64

	recent []activityEvent

	rdb *redis.Client
	log *slog.Logger
}

// NewRecorder returns a recorder scoped to this process.
func NewRecorder() *Recorder {
	return &Recorder{
		startedAt:  time.Now().UTC(),
		byProvider: make(map[string]int64),
	}
}

func (r *Recorder) appendEvent(e activityEvent) {
	r.recent = append(r.recent, e)
	if len(r.recent) > MaxRecentEvents {
		r.recent = r.recent[len(r.recent)-MaxRecentEvents:]
	}
}

func (r *Recorder) bumpProvider(provider string) {
	if provider != "" {
		r.byProvider[provider]++
	}
}

// RecordFastFail records a request blocked because the breaker is open.
func (r *Recorder) RecordFastFail(provider, key string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e := activityEvent{
		Time:     now.Unix(),
		Provider: provider,
		Key:      key,
		Kind:     EventFastFail,
		NewState: "open",
	}
	if r.redisEnabled() {
		r.recordRedisEvent("blocked_open", provider, e)
		return
	}
	r.mu.Lock()
	r.blockedOpen++
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.mu.Unlock()
}

// RecordProbe records a half-open probe dispatched to the upstream provider.
func (r *Recorder) RecordProbe(provider, key string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e := activityEvent{
		Time:     now.Unix(),
		Provider: provider,
		Key:      key,
		Kind:     EventProbe,
		NewState: "half_open",
	}
	if r.redisEnabled() {
		r.recordRedisEvent("probes_started", provider, e)
		return
	}
	r.mu.Lock()
	r.probesStarted++
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.mu.Unlock()
}

// RecordProbeClosed records a successful half-open probe (circuit closed).
func (r *Recorder) RecordProbeClosed(provider, key string, statusCode int) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e := activityEvent{
		Time:       now.Unix(),
		Provider:   provider,
		Key:        key,
		Kind:       EventProbeClosed,
		NewState:   "closed",
		StatusCode: statusCode,
	}
	if r.redisEnabled() {
		r.recordRedisEvent("probes_succeeded", provider, e)
		return
	}
	r.mu.Lock()
	r.probesSucceeded++
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.mu.Unlock()
}

// RecordProbeReopened records a failed half-open probe (circuit open again).
func (r *Recorder) RecordProbeReopened(provider, key string, statusCode int, failureKind string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e := activityEvent{
		Time:        now.Unix(),
		Provider:    provider,
		Key:         key,
		Kind:        EventProbeReopened,
		NewState:    "open",
		StatusCode:  statusCode,
		FailureKind: failureKind,
	}
	if r.redisEnabled() {
		r.recordRedisEvent("probes_failed", provider, e)
		return
	}
	r.mu.Lock()
	r.probesFailed++
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.mu.Unlock()
}

// RecordOpened records a circuit trip (failure threshold or force-open).
func (r *Recorder) RecordOpened(provider, key, reason string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e := activityEvent{
		Time:     now.Unix(),
		Provider: provider,
		Key:      key,
		Kind:     EventOpened,
		NewState: "open",
		Reason:   reason,
	}
	if r.redisEnabled() {
		r.recordRedisEvent("circuits_opened", provider, e)
		return
	}
	r.mu.Lock()
	r.circuitsOpened++
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.mu.Unlock()
}

// RecordCheck increments the state-check counter (one per routing decision).
func (r *Recorder) RecordCheck() {
	if r == nil {
		return
	}
	if r.redisEnabled() {
		r.incrRedisCheckAsync()
		return
	}
	r.mu.Lock()
	r.checksTotal++
	r.mu.Unlock()
}

// Snapshot returns JSON for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}
	if r.redisEnabled() {
		return r.snapshotRedis()
	}
	return r.snapshotMemory()
}

func (r *Recorder) snapshotMemory() map[string]interface{} {
	r.mu.RLock()
	recent := make([]activityEvent, len(r.recent))
	for i, e := range r.recent {
		recent[len(r.recent)-1-i] = e
	}
	byProvider := make(map[string]int64, len(r.byProvider))
	for k, v := range r.byProvider {
		byProvider[k] = v
	}
	snap := map[string]interface{}{
		"available":        true,
		"backend":          "memory",
		"started_at":       r.startedAt.Unix(),
		"checks_total":     r.checksTotal,
		"blocked_open":     r.blockedOpen,
		"probes_started":   r.probesStarted,
		"probes_succeeded": r.probesSucceeded,
		"probes_failed":    r.probesFailed,
		"circuits_opened":  r.circuitsOpened,
		"by_provider":      byProvider,
		"recent_events":    recent,
	}
	r.mu.RUnlock()
	return snap
}
