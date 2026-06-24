package modelstatusstats

import (
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
)

var modelStatusRollupCaps = adminrollup.TopNCaps{}

type kv struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type statusFlushed struct {
	retiredTotal    int64
	deprecatedTotal int64
	unknownTotal    int64
	retired         map[string]int64
	deprecated      map[string]int64
	unknown         map[string]int64
}

// Recorder accumulates retired, deprecated, and unknown model call counts
// in-process and publishes deltas to admin rollups for fleet-wide visibility.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	retiredTotal    int64
	deprecatedTotal int64
	unknownTotal    int64

	retired    map[string]int64
	deprecated map[string]int64
	unknown    map[string]int64

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
	}
}

func composeKey(provider, model string) string {
	return provider + ":" + model
}

func topN(m map[string]int64, n int) []kv {
	out := make([]kv, 0, len(m))
	for name, count := range m {
		out = append(out, kv{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func intMapDelta(cur, prev map[string]int64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range cur {
		if dv := float64(v - prev[k]); dv != 0 {
			out[k] = dv
		}
	}
	return out
}

func copyIntMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricModelStatus, oldDay, modelStatusRollupCaps)
	}()
	r.flushed = statusFlushed{}
	r.retiredTotal = 0
	r.deprecatedTotal = 0
	r.unknownTotal = 0
	r.retired = make(map[string]int64)
	r.deprecated = make(map[string]int64)
	r.unknown = make(map[string]int64)
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
		},
		Dimensions: map[string]map[string]float64{
			"by_retired":    intMapDelta(r.retired, r.flushed.retired),
			"by_deprecated": intMapDelta(r.deprecated, r.flushed.deprecated),
			"by_unknown":    intMapDelta(r.unknown, r.flushed.unknown),
		},
	}
}

func (r *Recorder) advanceFlushedLocked() {
	r.flushed.retiredTotal = r.retiredTotal
	r.flushed.deprecatedTotal = r.deprecatedTotal
	r.flushed.unknownTotal = r.unknownTotal
	r.flushed.retired = copyIntMap(r.retired)
	r.flushed.deprecated = copyIntMap(r.deprecated)
	r.flushed.unknown = copyIntMap(r.unknown)
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

// Snapshot returns a JSON-serialisable view for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}

	today := time.Now().UTC().Format("2006-01-02")

	r.mu.RLock()
	bucketDay := r.dayKey
	localActive := bucketDay == today
	startedAt := r.startedAt

	var retiredTotal, deprecatedTotal, unknownTotal int64
	var localRetired, localDeprecated, localUnknown map[string]int64
	if localActive {
		retiredTotal = r.retiredTotal
		deprecatedTotal = r.deprecatedTotal
		unknownTotal = r.unknownTotal
		localRetired = copyIntMap(r.retired)
		localDeprecated = copyIntMap(r.deprecated)
		localUnknown = copyIntMap(r.unknown)
	}

	backend := "memory"
	if r.RollupBound() {
		backend = "redis"
	}
	snap := map[string]interface{}{
		"available":        true,
		"backend":          backend,
		"day":              today,
		"started_at":       startedAt.Unix(),
		"retired_total":    retiredTotal,
		"deprecated_total": deprecatedTotal,
		"unknown_total":    unknownTotal,
		"by_retired":       topN(localRetired, 0),
		"by_deprecated":    topN(localDeprecated, 0),
		"by_unknown":       topN(localUnknown, 0),
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricModelStatus, today, snap, modelStatusRollupCaps)
	if localActive {
		mergeLocalModelStatusIntoSnap(snap, retiredTotal, deprecatedTotal, unknownTotal, localRetired, localDeprecated, localUnknown)
	}
	r.MergeHistory(adminrollup.MetricModelStatus, snap)
	r.MergeHourly(adminrollup.MetricModelStatus, snap)
	return snap
}

func mergeLocalModelStatusIntoSnap(
	snap map[string]interface{},
	retiredTotal, deprecatedTotal, unknownTotal int64,
	localRetired, localDeprecated, localUnknown map[string]int64,
) {
	adminrollup.MergeSnapInt64Max(snap, "retired_total", retiredTotal)
	adminrollup.MergeSnapInt64Max(snap, "deprecated_total", deprecatedTotal)
	adminrollup.MergeSnapInt64Max(snap, "unknown_total", unknownTotal)
	mergeModelStatusNameCounts(snap, "by_retired", localRetired, 0)
	mergeModelStatusNameCounts(snap, "by_deprecated", localDeprecated, 0)
	mergeModelStatusNameCounts(snap, "by_unknown", localUnknown, 0)
}

func mergeModelStatusNameCounts(snap map[string]interface{}, field string, local map[string]int64, limit int) {
	if snap == nil || len(local) == 0 {
		return
	}
	merged := adminrollup.MergeInt64Maps(adminrollup.NameCountMapFromSnap(snap[field]), local)
	if len(merged) == 0 {
		return
	}
	snap[field] = topN(merged, limit)
}
