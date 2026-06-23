// Package pii provides an in-process, dependency-free aggregator for PII
// redaction outcomes. Optional Redis rollups via adminrollup.
package pii

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/history"
)

// MaxRecentEvents bounds the recent-events ring buffer so memory stays
// flat under arbitrarily high request volume.
const MaxRecentEvents = 50

// Outcome enumerates the terminal states of a redaction attempt.
const (
	OutcomeOK         = "ok"
	OutcomeFailOpen   = "fail_open"
	OutcomeFailClosed = "fail_closed"
	OutcomeOversize   = "oversize"
)

type recentEntry struct {
	Time         int64          `json:"time"`
	Provider     string         `json:"provider"`
	KeyID        string         `json:"key_id,omitempty"`
	EntityCounts map[string]int `json:"entity_counts"`
	EntityTotal  int            `json:"entity_total"`
	BodyBytes    int            `json:"body_bytes"`
	DurationMs   float64        `json:"duration_ms"`
	Outcome      string         `json:"outcome"`
}

// Recorder accumulates rolling PII detection stats in-process.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	requestsScanned int64
	requestsWithPII int64
	entitiesTotal   int64
	failOpen        int64
	failClosed      int64
	oversize        int64

	byEntity   map[string]int64
	byProvider map[string]int64
	byKey      map[string]int64

	recent  []recentEntry
	flushed piiFlushed

	// Shared Redis rollup lifecycle; promoted methods satisfy the recorder's
	// public BindRollup/FlushRollup API.
	adminrollup.RecorderBinding
	history.Binding
}

type piiFlushed struct {
	requestsScanned, requestsWithPII, entitiesTotal int64
	failOpen, failClosed, oversize                  int64
	byEntity, byProvider, byKey                     map[string]int64
}

var piiRollupCaps = adminrollup.TopNCaps{ByKey: 100}

// NewRecorder returns a ready-to-use Recorder.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		byEntity:   make(map[string]int64),
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricPII, oldDay, piiRollupCaps)
	}()
	r.flushed = piiFlushed{}
	r.requestsScanned = 0
	r.requestsWithPII = 0
	r.entitiesTotal = 0
	r.failOpen = 0
	r.failClosed = 0
	r.oversize = 0
	r.byEntity = make(map[string]int64)
	r.byProvider = make(map[string]int64)
	r.byKey = make(map[string]int64)
	r.recent = nil
}

type kv struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
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

// detectionRateLocked returns the count of cleanly-scanned requests (total
// scans minus fail-open/fail-closed/oversize, which never produced a real
// detection result) and the share of those that contained PII. Caller must
// hold r.mu. Single source of truth so the persisted rollup and the live
// Snapshot can't diverge.
func (r *Recorder) detectionRateLocked() (int64, float64) {
	cleanScanned := r.requestsScanned - r.failOpen - r.failClosed - r.oversize
	var rate float64
	if cleanScanned > 0 {
		rate = float64(r.requestsWithPII) / float64(cleanScanned)
	}
	return cleanScanned, rate
}

func (r *Recorder) rollupDataLocked() map[string]interface{} {
	_, detectionRate := r.detectionRateLocked()
	return map[string]interface{}{
		"requests_scanned":  r.requestsScanned,
		"requests_with_pii": r.requestsWithPII,
		"entities_total":    r.entitiesTotal,
		"detection_rate":    detectionRate,
		"fail_open":         r.failOpen,
		"fail_closed":       r.failClosed,
		"oversize":          r.oversize,
		"by_entity":         topN(r.byEntity, 0),
		"by_provider":       topN(r.byProvider, 0),
		"top_keys":          topN(r.byKey, 10),
	}
}

// RecordRedaction ingests a single redaction outcome.
func (r *Recorder) RecordRedaction(
	provider, keyID string,
	entityCounts map[string]int,
	bodyBytes int,
	duration time.Duration,
	outcome string,
) {
	if r == nil {
		return
	}

	entityTotal := 0
	for _, n := range entityCounts {
		entityTotal += n
	}

	now := time.Now().UTC()
	r.mu.Lock()

	r.maybeRollDay(now)

	r.requestsScanned++
	if provider != "" {
		r.byProvider[provider]++
	}
	if keyID != "" {
		r.byKey[keyID]++
	}

	switch outcome {
	case OutcomeFailOpen:
		r.failOpen++
	case OutcomeFailClosed:
		r.failClosed++
	case OutcomeOversize:
		r.oversize++
	}

	if entityTotal > 0 {
		r.requestsWithPII++
		r.entitiesTotal += int64(entityTotal)
		for entity, n := range entityCounts {
			r.byEntity[entity] += int64(n)
		}
	}

	entry := recentEntry{
		Time:         now.Unix(),
		Provider:     provider,
		KeyID:        keyID,
		EntityCounts: entityCounts,
		EntityTotal:  entityTotal,
		BodyBytes:    bodyBytes,
		DurationMs:   float64(duration.Microseconds()) / 1000.0,
		Outcome:      outcome,
	}
	r.recent = append(r.recent, entry)
	if len(r.recent) > MaxRecentEvents {
		r.recent = r.recent[len(r.recent)-MaxRecentEvents:]
	}
	r.EmitHistory(entry)

	dayKey := r.dayKey
	delta := r.piiDeltaLocked()
	r.advancePIIFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
	if r.RollupBound() {
		r.AppendRecentEvent(adminrollup.MetricPII, entry, MaxRecentEvents)
	}
}

func (r *Recorder) piiDeltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"requests_scanned":  float64(r.requestsScanned - r.flushed.requestsScanned),
			"requests_with_pii": float64(r.requestsWithPII - r.flushed.requestsWithPII),
			"entities_total":    float64(r.entitiesTotal - r.flushed.entitiesTotal),
			"fail_open":         float64(r.failOpen - r.flushed.failOpen),
			"fail_closed":       float64(r.failClosed - r.flushed.failClosed),
			"oversize":          float64(r.oversize - r.flushed.oversize),
		},
		Dimensions: map[string]map[string]float64{
			"by_entity":   intMapDelta(r.byEntity, r.flushed.byEntity),
			"by_provider": intMapDelta(r.byProvider, r.flushed.byProvider),
			"by_key":      intMapDelta(r.byKey, r.flushed.byKey),
		},
	}
	return d
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

func (r *Recorder) advancePIIFlushedLocked() {
	r.flushed.requestsScanned = r.requestsScanned
	r.flushed.requestsWithPII = r.requestsWithPII
	r.flushed.entitiesTotal = r.entitiesTotal
	r.flushed.failOpen = r.failOpen
	r.flushed.failClosed = r.failClosed
	r.flushed.oversize = r.oversize
	r.flushed.byEntity = copyIntMap(r.byEntity)
	r.flushed.byProvider = copyIntMap(r.byProvider)
	r.flushed.byKey = copyIntMap(r.byKey)
}

func copyIntMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Snapshot returns a JSON-serialisable view of the current aggregates for
// the admin API. Recent events are returned newest-first.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}

	r.mu.RLock()
	recent := make([]recentEntry, len(r.recent))
	for i, e := range r.recent {
		recent[len(r.recent)-1-i] = e
	}

	_, detectionRate := r.detectionRateLocked()

	dayKey := r.dayKey
	snap := map[string]interface{}{
		"available":         true,
		"day":               dayKey,
		"started_at":        r.startedAt.Unix(),
		"requests_scanned":  r.requestsScanned,
		"requests_with_pii": r.requestsWithPII,
		"entities_total":    r.entitiesTotal,
		"detection_rate":    detectionRate,
		"fail_open":         r.failOpen,
		"fail_closed":       r.failClosed,
		"oversize":          r.oversize,
		"by_entity":         topN(r.byEntity, 0),
		"by_provider":       topN(r.byProvider, 0),
		"top_keys":          topN(r.byKey, 10),
		"recent":            recent,
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricPII, dayKey, snap, piiRollupCaps)
	r.MergeHistory(adminrollup.MetricPII, snap)
	r.MergeRecentEvents(adminrollup.MetricPII, "recent", MaxRecentEvents, snap, parseRecentEventPayloads)
	if _, ok := snap["recent_backend"]; !ok {
		snap["recent_backend"] = "memory"
	}
	return snap
}

func parseRecentEventPayloads(raw []json.RawMessage) any {
	recent := make([]recentEntry, 0, len(raw))
	for _, payload := range raw {
		var e recentEntry
		if json.Unmarshal(payload, &e) == nil {
			recent = append(recent, e)
		}
	}
	return recent
}
