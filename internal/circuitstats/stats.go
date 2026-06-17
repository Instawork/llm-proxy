// Package circuitstats records circuit-breaker activity for the admin dashboard:
// state checks, fast-fails while open, and half-open probe outcomes when a
// cooldown window ends. Counters are published to admin rollups (Redis DB 6)
// so sidecars aggregate fleet-wide; recent events may also be mirrored on the
// circuit-breaker Redis DB when a shared client is available.
package circuitstats

import (
	"log/slog"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
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

type activityFlushed struct {
	checksTotal     int64
	blockedOpen     int64
	probesStarted   int64
	probesSucceeded int64
	probesFailed    int64
	circuitsOpened  int64
	byProvider      map[string]int64
	byKey           map[string]int64
}

// Recorder accumulates rolling circuit activity. Each instance keeps local
// counters and publishes deltas to admin rollups; optional Redis mirrors the
// recent-events ring across tasks on the circuit-breaker DB.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	checksTotal     int64
	blockedOpen     int64
	probesStarted   int64
	probesSucceeded int64
	probesFailed    int64
	circuitsOpened  int64

	byProvider map[string]int64
	byKey      map[string]int64

	recent  []activityEvent
	flushed activityFlushed

	adminrollup.RecorderBinding

	rdb *redis.Client
	log *slog.Logger
}

// NewRecorder returns a recorder scoped to this process.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		byProvider: make(map[string]int64),
		byKey:      make(map[string]int64),
	}
}

func (r *Recorder) maybeRollDay(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if r.dayKey == day {
		return
	}
	oldDay := r.dayKey
	r.dayKey = day
	r.FlushRollup()
	go func() {
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricCircuitActivity, oldDay, adminrollup.TopNCaps{})
	}()
	r.flushed = activityFlushed{}
	r.checksTotal = 0
	r.blockedOpen = 0
	r.probesStarted = 0
	r.probesSucceeded = 0
	r.probesFailed = 0
	r.circuitsOpened = 0
	r.byProvider = make(map[string]int64)
	r.byKey = make(map[string]int64)
	r.recent = nil
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

func (r *Recorder) bumpBlockedKey(key string) {
	if key != "" {
		r.byKey[key]++
	}
}

func (r *Recorder) activityDeltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"checks_total":     float64(r.checksTotal - r.flushed.checksTotal),
			"blocked_open":     float64(r.blockedOpen - r.flushed.blockedOpen),
			"probes_started":   float64(r.probesStarted - r.flushed.probesStarted),
			"probes_succeeded": float64(r.probesSucceeded - r.flushed.probesSucceeded),
			"probes_failed":    float64(r.probesFailed - r.flushed.probesFailed),
			"circuits_opened":  float64(r.circuitsOpened - r.flushed.circuitsOpened),
		},
	}
	if provDelta := int64MapDelta(r.byProvider, r.flushed.byProvider); len(provDelta) > 0 {
		if d.Dimensions == nil {
			d.Dimensions = make(map[string]map[string]float64)
		}
		d.Dimensions["by_provider"] = provDelta
	}
	if keyDelta := int64MapDelta(r.byKey, r.flushed.byKey); len(keyDelta) > 0 {
		if d.Dimensions == nil {
			d.Dimensions = make(map[string]map[string]float64)
		}
		d.Dimensions["by_key"] = keyDelta
	}
	return d
}

func int64MapDelta(cur map[string]int64, prev map[string]int64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range cur {
		p := prev[k]
		if dr := float64(v - p); dr != 0 {
			out[k] = dr
		}
	}
	return out
}

func (r *Recorder) advanceFlushedLocked() {
	if r.flushed.byProvider == nil {
		r.flushed.byProvider = make(map[string]int64)
	}
	if r.flushed.byKey == nil {
		r.flushed.byKey = make(map[string]int64)
	}
	r.flushed.checksTotal = r.checksTotal
	r.flushed.blockedOpen = r.blockedOpen
	r.flushed.probesStarted = r.probesStarted
	r.flushed.probesSucceeded = r.probesSucceeded
	r.flushed.probesFailed = r.probesFailed
	r.flushed.circuitsOpened = r.circuitsOpened
	for k, v := range r.byProvider {
		r.flushed.byProvider[k] = v
	}
	for k, v := range r.byKey {
		r.flushed.byKey[k] = v
	}
}

func (r *Recorder) publishLocked() {
	dayKey := r.dayKey
	delta := r.activityDeltaLocked()
	r.advanceFlushedLocked()
	r.mu.Unlock()
	r.QueueDelta(dayKey, delta)
	r.mu.Lock()
}

func (r *Recorder) recordActivity(counterField, provider string, e activityEvent, apply func()) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	e.Time = now.Unix()

	r.mu.Lock()
	r.maybeRollDay(now)
	apply()
	r.bumpProvider(provider)
	r.appendEvent(e)
	r.publishLocked()
	r.mu.Unlock()

	if r.redisEnabled() {
		r.recordRedisEvent(counterField, provider, e)
	}
}

// RecordFastFail records a request blocked because the breaker is open.
func (r *Recorder) RecordFastFail(provider, key string) {
	e := activityEvent{
		Provider: provider,
		Key:      key,
		Kind:     EventFastFail,
		NewState: "open",
	}
	r.recordActivity("blocked_open", provider, e, func() {
		r.blockedOpen++
		r.bumpBlockedKey(key)
	})
}

// RecordProbe records a half-open probe dispatched to the upstream provider.
func (r *Recorder) RecordProbe(provider, key string) {
	e := activityEvent{
		Provider: provider,
		Key:      key,
		Kind:     EventProbe,
		NewState: "half_open",
	}
	r.recordActivity("probes_started", provider, e, func() {
		r.probesStarted++
	})
}

// RecordProbeClosed records a successful half-open probe (circuit closed).
func (r *Recorder) RecordProbeClosed(provider, key string, statusCode int) {
	e := activityEvent{
		Provider:   provider,
		Key:        key,
		Kind:       EventProbeClosed,
		NewState:   "closed",
		StatusCode: statusCode,
	}
	r.recordActivity("probes_succeeded", provider, e, func() {
		r.probesSucceeded++
	})
}

// RecordProbeReopened records a failed half-open probe (circuit open again).
func (r *Recorder) RecordProbeReopened(provider, key string, statusCode int, failureKind string) {
	e := activityEvent{
		Provider:    provider,
		Key:         key,
		Kind:        EventProbeReopened,
		NewState:    "open",
		StatusCode:  statusCode,
		FailureKind: failureKind,
	}
	r.recordActivity("probes_failed", provider, e, func() {
		r.probesFailed++
	})
}

// RecordOpened records a circuit trip (failure threshold or force-open).
func (r *Recorder) RecordOpened(provider, key, reason string) {
	e := activityEvent{
		Provider: provider,
		Key:      key,
		Kind:     EventOpened,
		NewState: "open",
		Reason:   reason,
	}
	r.recordActivity("circuits_opened", provider, e, func() {
		r.circuitsOpened++
	})
}

// RecordCheck increments the state-check counter (one per routing decision).
func (r *Recorder) RecordCheck() {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.maybeRollDay(now)
	r.checksTotal++
	r.publishLocked()
	r.mu.Unlock()

	if r.redisEnabled() {
		r.incrRedisCheckAsync()
	}
}

// Snapshot returns JSON for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}
	r.mu.RLock()
	dayKey := r.dayKey
	snap := r.snapshotMemoryLocked()
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricCircuitActivity, dayKey, snap, adminrollup.TopNCaps{})
	r.MergeHistory(adminrollup.MetricCircuitActivity, snap)
	r.mergeRedisRecentEvents(snap)
	return snap
}

func (r *Recorder) snapshotMemoryLocked() map[string]interface{} {
	recent := make([]activityEvent, len(r.recent))
	for i, e := range r.recent {
		recent[len(r.recent)-1-i] = e
	}
	byProvider := make(map[string]int64, len(r.byProvider))
	for k, v := range r.byProvider {
		byProvider[k] = v
	}
	byKey := make(map[string]int64, len(r.byKey))
	for k, v := range r.byKey {
		byKey[k] = v
	}
	backend := "memory"
	if r.redisEnabled() {
		backend = "redis"
	}
	return map[string]interface{}{
		"available":        true,
		"backend":          backend,
		"day":              r.dayKey,
		"started_at":       r.startedAt.Unix(),
		"checks_total":     r.checksTotal,
		"blocked_open":     r.blockedOpen,
		"probes_started":   r.probesStarted,
		"probes_succeeded": r.probesSucceeded,
		"probes_failed":    r.probesFailed,
		"circuits_opened":  r.circuitsOpened,
		"by_provider":      byProvider,
		"by_key":           byKey,
		"recent_events":    recent,
	}
}
