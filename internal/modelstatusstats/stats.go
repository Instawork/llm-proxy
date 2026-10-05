package modelstatusstats

import (
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
)

var modelStatusRollupCaps = adminrollup.TopNCaps{}

type statusFlushed struct {
	retiredTotal    int64
	deprecatedTotal int64
	unknownTotal    int64
	unmeteredTotal  int64
	retired         map[string]int64
	deprecated      map[string]int64
	unknown         map[string]int64
	unmetered       map[string]int64
}

// Recorder accumulates retired, deprecated, and unknown model call counts,
// plus calls to provider endpoints the proxy does not meter, in-process and publishes deltas to admin rollups for fleet-wide visibility.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	retiredTotal    int64
	deprecatedTotal int64
	unknownTotal    int64
	unmeteredTotal  int64

	retired    map[string]int64
	deprecated map[string]int64
	unknown    map[string]int64
	unmetered  map[string]int64

	flushed statusFlushed

	adminrollup.RecorderBinding
}

// NewRecorder returns a ready-to-use recorder.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		retired:    make(map[string]int64),
		deprecated: make(map[string]int64),
		unknown:    make(map[string]int64),
		unmetered:  make(map[string]int64),
	}
}

func composeKey(provider, model string) string {
	return provider + ":" + model
}

func (r *Recorder) maybeRollDay(now time.Time) {
	// RollDay swaps the day key under r.mu and runs flush + archive off
	// the lock; only the in-memory reset below happens here.
	if !r.RollDay(&r.dayKey, now, adminrollup.MetricModelStatus, modelStatusRollupCaps) {
		return
	}
	r.flushed = statusFlushed{}
	r.retiredTotal = 0
	r.deprecatedTotal = 0
	r.unknownTotal = 0
	r.unmeteredTotal = 0
	r.retired = make(map[string]int64)
	r.deprecated = make(map[string]int64)
	r.unknown = make(map[string]int64)
	r.unmetered = make(map[string]int64)
}

func (r *Recorder) bumpLocked(counter map[string]int64, provider, model string) {
	if provider == "" || model == "" {
		return
	}
	counter[composeKey(provider, model)]++
}

func (r *Recorder) statusDeltaLocked() adminrollup.Delta {
	return adminrollup.Delta{
		Totals: map[string]float64{
			"retired_total":    float64(r.retiredTotal - r.flushed.retiredTotal),
			"deprecated_total": float64(r.deprecatedTotal - r.flushed.deprecatedTotal),
			"unknown_total":    float64(r.unknownTotal - r.flushed.unknownTotal),
			"unmetered_total":  float64(r.unmeteredTotal - r.flushed.unmeteredTotal),
		},
		Dimensions: map[string]map[string]float64{
			"by_retired":    adminrollup.IntMapDelta(r.retired, r.flushed.retired),
			"by_deprecated": adminrollup.IntMapDelta(r.deprecated, r.flushed.deprecated),
			"by_unknown":    adminrollup.IntMapDelta(r.unknown, r.flushed.unknown),
			"by_unmetered":  adminrollup.IntMapDelta(r.unmetered, r.flushed.unmetered),
		},
	}
}

func (r *Recorder) advanceFlushedLocked() {
	r.flushed.retiredTotal = r.retiredTotal
	r.flushed.deprecatedTotal = r.deprecatedTotal
	r.flushed.unknownTotal = r.unknownTotal
	r.flushed.unmeteredTotal = r.unmeteredTotal
	r.flushed.retired = adminrollup.CopyIntMap(r.retired)
	r.flushed.deprecated = adminrollup.CopyIntMap(r.deprecated)
	r.flushed.unknown = adminrollup.CopyIntMap(r.unknown)
	r.flushed.unmetered = adminrollup.CopyIntMap(r.unmetered)
}

func (r *Recorder) publishLocked() {
	dayKey := r.dayKey
	delta := r.statusDeltaLocked()
	r.advanceFlushedLocked()
	r.mu.Unlock()
	r.QueueDelta(dayKey, delta)
	r.mu.Lock()
}

func (r *Recorder) record(total *int64, counter map[string]int64, provider, model string) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.maybeRollDay(now)
	*total++
	r.bumpLocked(counter, provider, model)
	r.publishLocked()
	r.mu.Unlock()
}

// RecordRetired increments the retired-model counter.
func (r *Recorder) RecordRetired(provider, model string) {
	r.record(&r.retiredTotal, r.retired, provider, model)
}

// RecordDeprecated increments the deprecated-model counter.
func (r *Recorder) RecordDeprecated(provider, model string) {
	r.record(&r.deprecatedTotal, r.deprecated, provider, model)
}

// RecordUnknown increments the unrecognized-model counter.
func (r *Recorder) RecordUnknown(provider, model string) {
	r.record(&r.unknownTotal, r.unknown, provider, model)
}

// RecordUnmetered increments the counter for a provider endpoint template
// the proxy forwards without parsing token usage.
func (r *Recorder) RecordUnmetered(provider, endpoint string) {
	r.record(&r.unmeteredTotal, r.unmetered, provider, endpoint)
}

// Snapshot returns a JSON-serialisable view for the admin API.
func (r *Recorder) Snapshot() map[string]any {
	if r == nil {
		return map[string]any{"available": false}
	}

	today := time.Now().UTC().Format("2006-01-02")

	r.mu.RLock()
	bucketDay := r.dayKey
	localActive := bucketDay == today
	startedAt := r.startedAt

	var retiredTotal, deprecatedTotal, unknownTotal, unmeteredTotal int64
	var localRetired, localDeprecated, localUnknown, localUnmetered map[string]int64
	if localActive {
		retiredTotal = r.retiredTotal
		deprecatedTotal = r.deprecatedTotal
		unknownTotal = r.unknownTotal
		unmeteredTotal = r.unmeteredTotal
		localRetired = adminrollup.CopyIntMap(r.retired)
		localDeprecated = adminrollup.CopyIntMap(r.deprecated)
		localUnknown = adminrollup.CopyIntMap(r.unknown)
		localUnmetered = adminrollup.CopyIntMap(r.unmetered)
	}

	backend := "memory"
	if r.RollupBound() {
		backend = "redis"
	}
	snap := map[string]any{
		"available":        true,
		"backend":          backend,
		"day":              today,
		"started_at":       startedAt.Unix(),
		"retired_total":    retiredTotal,
		"deprecated_total": deprecatedTotal,
		"unknown_total":    unknownTotal,
		"unmetered_total":  unmeteredTotal,
		"by_retired":       adminrollup.TopN(localRetired, 0),
		"by_deprecated":    adminrollup.TopN(localDeprecated, 0),
		"by_unknown":       adminrollup.TopN(localUnknown, 0),
		"by_unmetered":     adminrollup.TopN(localUnmetered, 0),
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricModelStatus, today, snap, modelStatusRollupCaps)
	if localActive {
		mergeLocalModelStatusIntoSnap(snap, retiredTotal, deprecatedTotal, unknownTotal, unmeteredTotal, localRetired, localDeprecated, localUnknown, localUnmetered)
	}
	r.MergeHistory(adminrollup.MetricModelStatus, snap)
	r.MergeHourly(adminrollup.MetricModelStatus, snap)
	return snap
}

func mergeLocalModelStatusIntoSnap(
	snap map[string]any,
	retiredTotal, deprecatedTotal, unknownTotal, unmeteredTotal int64,
	localRetired, localDeprecated, localUnknown, localUnmetered map[string]int64,
) {
	adminrollup.MergeSnapInt64Max(snap, "retired_total", retiredTotal)
	adminrollup.MergeSnapInt64Max(snap, "deprecated_total", deprecatedTotal)
	adminrollup.MergeSnapInt64Max(snap, "unknown_total", unknownTotal)
	adminrollup.MergeSnapInt64Max(snap, "unmetered_total", unmeteredTotal)
	mergeModelStatusNameCounts(snap, "by_retired", localRetired, 0)
	mergeModelStatusNameCounts(snap, "by_deprecated", localDeprecated, 0)
	mergeModelStatusNameCounts(snap, "by_unknown", localUnknown, 0)
	mergeModelStatusNameCounts(snap, "by_unmetered", localUnmetered, 0)
}

func mergeModelStatusNameCounts(snap map[string]any, field string, local map[string]int64, limit int) {
	if snap == nil || len(local) == 0 {
		return
	}
	merged := adminrollup.MergeInt64Maps(adminrollup.NameCountMapFromSnap(snap[field]), local)
	if len(merged) == 0 {
		return
	}
	snap[field] = adminrollup.TopN(merged, limit)
}
