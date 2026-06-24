// Package idgatestats records government-ID gate outcomes for the admin
// dashboard, Redis rollups, and optional S3 row history.
package idgatestats

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/history"
)

const MaxRecentEvents = 50

const (
	OutcomeClear      = "clear"
	OutcomeBlocked    = "blocked"
	OutcomeFailOpen   = "fail_open"
	OutcomeFailClosed = "fail_closed"
)

type recentEntry struct {
	Time       int64   `json:"time"`
	Provider   string  `json:"provider"`
	KeyID      string  `json:"key_id,omitempty"`
	Outcome    string  `json:"outcome"`
	EntityType string  `json:"entity_type,omitempty"`
	Score      float64 `json:"score,omitempty"`
	ImageCount int     `json:"image_count,omitempty"`
	ImageIndex int     `json:"image_index,omitempty"`
	Stage      string  `json:"stage,omitempty"`
	DurationMs float64 `json:"duration_ms"`
	Pipeline   string  `json:"pipeline"`
}

// Recorder accumulates ID gate stats in-process with optional Redis rollups.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	requestsWithImages int64
	requestsBlocked    int64
	requestsCleared    int64
	failOpen           int64
	failClosed         int64
	imagesScanned      int64

	byProvider map[string]int64
	byEntity   map[string]int64
	byKey      map[string]int64

	recent  []recentEntry
	flushed idGateFlushed

	adminrollup.RecorderBinding
	history.Binding
}

type idGateFlushed struct {
	requestsWithImages, requestsBlocked, requestsCleared int64
	failOpen, failClosed, imagesScanned                  int64
	byProvider, byEntity, byKey                          map[string]int64
}

var idGateRollupCaps = adminrollup.TopNCaps{ByKey: 100}

// NewRecorder returns a recorder scoped to the current UTC day.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		byProvider: make(map[string]int64),
		byEntity:   make(map[string]int64),
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricIDGate, oldDay, idGateRollupCaps)
	}()
	r.flushed = idGateFlushed{}
	r.requestsWithImages = 0
	r.requestsBlocked = 0
	r.requestsCleared = 0
	r.failOpen = 0
	r.failClosed = 0
	r.imagesScanned = 0
	r.byProvider = make(map[string]int64)
	r.byEntity = make(map[string]int64)
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

func (r *Recorder) rollupDataLocked() map[string]interface{} {
	return map[string]interface{}{
		"requests_with_images": r.requestsWithImages,
		"requests_blocked":     r.requestsBlocked,
		"requests_cleared":     r.requestsCleared,
		"fail_open":            r.failOpen,
		"fail_closed":          r.failClosed,
		"images_scanned":       r.imagesScanned,
		"by_provider":          topN(r.byProvider, 0),
		"by_entity":            topN(r.byEntity, 0),
		"top_keys":             topN(r.byKey, 10),
	}
}

func (r *Recorder) ingest(entry recentEntry) {
	now := time.Now().UTC()
	r.mu.Lock()
	r.maybeRollDay(now)

	r.requestsWithImages++
	r.imagesScanned += int64(entry.ImageCount)
	if provider := entry.Provider; provider != "" {
		r.byProvider[provider]++
	}
	if keyID := entry.KeyID; keyID != "" {
		r.byKey[keyID]++
	}

	switch entry.Outcome {
	case OutcomeBlocked:
		r.requestsBlocked++
		if entry.EntityType != "" {
			r.byEntity[entry.EntityType]++
		}
	case OutcomeClear:
		r.requestsCleared++
	case OutcomeFailOpen:
		r.failOpen++
	case OutcomeFailClosed:
		r.failClosed++
	}

	r.recent = append(r.recent, entry)
	if len(r.recent) > MaxRecentEvents {
		r.recent = r.recent[len(r.recent)-MaxRecentEvents:]
	}
	r.EmitHistory(entry)

	dayKey := r.dayKey
	delta := r.deltaLocked()
	r.advanceFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
	if r.RollupBound() {
		r.AppendRecentEvent(adminrollup.MetricIDGate, entry, MaxRecentEvents)
	}
}

func (r *Recorder) deltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"requests_with_images": float64(r.requestsWithImages - r.flushed.requestsWithImages),
			"requests_blocked":     float64(r.requestsBlocked - r.flushed.requestsBlocked),
			"requests_cleared":     float64(r.requestsCleared - r.flushed.requestsCleared),
			"fail_open":            float64(r.failOpen - r.flushed.failOpen),
			"fail_closed":          float64(r.failClosed - r.flushed.failClosed),
			"images_scanned":       float64(r.imagesScanned - r.flushed.imagesScanned),
		},
		Dimensions: map[string]map[string]float64{
			"by_provider": intMapDelta(r.byProvider, r.flushed.byProvider),
			"by_entity":   intMapDelta(r.byEntity, r.flushed.byEntity),
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

func (r *Recorder) advanceFlushedLocked() {
	r.flushed.requestsWithImages = r.requestsWithImages
	r.flushed.requestsBlocked = r.requestsBlocked
	r.flushed.requestsCleared = r.requestsCleared
	r.flushed.failOpen = r.failOpen
	r.flushed.failClosed = r.failClosed
	r.flushed.imagesScanned = r.imagesScanned
	r.flushed.byProvider = copyIntMap(r.byProvider)
	r.flushed.byEntity = copyIntMap(r.byEntity)
	r.flushed.byKey = copyIntMap(r.byKey)
}

func copyIntMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// RecordClear logs a request whose embedded images were scanned and cleared.
func (r *Recorder) RecordClear(provider, keyID string, imageCount int, duration time.Duration) {
	if r == nil || imageCount <= 0 {
		return
	}
	r.ingest(recentEntry{
		Time:       time.Now().UTC().Unix(),
		Provider:   provider,
		KeyID:      keyID,
		Outcome:    OutcomeClear,
		ImageCount: imageCount,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		Pipeline:   "id_gate",
	})
}

// RecordBlocked logs a 422 government-ID block.
func (r *Recorder) RecordBlocked(provider, keyID, entityType string, score float64, imageIndex int, imageCount int, duration time.Duration) {
	if r == nil {
		return
	}
	r.ingest(recentEntry{
		Time:       time.Now().UTC().Unix(),
		Provider:   provider,
		KeyID:      keyID,
		Outcome:    OutcomeBlocked,
		EntityType: entityType,
		Score:      score,
		ImageCount: imageCount,
		ImageIndex: imageIndex,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		Pipeline:   "id_gate",
	})
}

// RecordScanFailed logs an OCR/analyze failure (fail-open or fail-closed).
func (r *Recorder) RecordScanFailed(provider, keyID, stage string, failClosed bool, imageCount int, duration time.Duration) {
	if r == nil {
		return
	}
	outcome := OutcomeFailOpen
	if failClosed {
		outcome = OutcomeFailClosed
	}
	r.ingest(recentEntry{
		Time:       time.Now().UTC().Unix(),
		Provider:   provider,
		KeyID:      keyID,
		Outcome:    outcome,
		Stage:      stage,
		ImageCount: imageCount,
		DurationMs: float64(duration.Microseconds()) / 1000.0,
		Pipeline:   "id_gate",
	})
}

// Snapshot returns JSON for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}

	today := time.Now().UTC().Format("2006-01-02")

	r.mu.RLock()
	bucketDay := r.dayKey
	localActive := bucketDay == today
	startedAt := r.startedAt

	var recent []recentEntry
	if localActive {
		recent = make([]recentEntry, len(r.recent))
		for i, e := range r.recent {
			recent[len(r.recent)-1-i] = e
		}
	}

	var requestsWithImages, requestsBlocked, requestsCleared int64
	var failOpen, failClosed, imagesScanned int64
	var localByProvider, localByEntity, localByKey map[string]int64
	if localActive {
		requestsWithImages = r.requestsWithImages
		requestsBlocked = r.requestsBlocked
		requestsCleared = r.requestsCleared
		failOpen = r.failOpen
		failClosed = r.failClosed
		imagesScanned = r.imagesScanned
		localByProvider = copyIntMap(r.byProvider)
		localByEntity = copyIntMap(r.byEntity)
		localByKey = copyIntMap(r.byKey)
	}

	snap := map[string]interface{}{
		"available":            true,
		"day":                  today,
		"started_at":           startedAt.Unix(),
		"requests_with_images": requestsWithImages,
		"requests_blocked":     requestsBlocked,
		"requests_cleared":     requestsCleared,
		"fail_open":            failOpen,
		"fail_closed":          failClosed,
		"images_scanned":       imagesScanned,
		"by_provider":          topN(localByProvider, 0),
		"by_entity":            topN(localByEntity, 0),
		"top_keys":             topN(localByKey, 10),
		"recent":               recent,
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricIDGate, today, snap, idGateRollupCaps)
	if localActive {
		mergeLocalIDGateIntoSnap(snap, requestsWithImages, requestsBlocked, requestsCleared, failOpen, failClosed, imagesScanned, localByProvider, localByEntity, localByKey)
	}
	r.MergeHistory(adminrollup.MetricIDGate, snap)
	r.MergeHourly(adminrollup.MetricIDGate, snap)
	r.MergeRecentEvents(adminrollup.MetricIDGate, "recent", MaxRecentEvents, snap, parseRecentEventPayloads)
	if _, ok := snap["recent_backend"]; !ok {
		snap["recent_backend"] = "memory"
	}
	return snap
}

func mergeLocalIDGateIntoSnap(
	snap map[string]interface{},
	requestsWithImages, requestsBlocked, requestsCleared int64,
	failOpen, failClosed, imagesScanned int64,
	localByProvider, localByEntity, localByKey map[string]int64,
) {
	adminrollup.MergeSnapInt64Max(snap, "requests_with_images", requestsWithImages)
	adminrollup.MergeSnapInt64Max(snap, "requests_blocked", requestsBlocked)
	adminrollup.MergeSnapInt64Max(snap, "requests_cleared", requestsCleared)
	adminrollup.MergeSnapInt64Max(snap, "fail_open", failOpen)
	adminrollup.MergeSnapInt64Max(snap, "fail_closed", failClosed)
	adminrollup.MergeSnapInt64Max(snap, "images_scanned", imagesScanned)
	mergeIDGateNameCounts(snap, "by_provider", localByProvider, 0)
	mergeIDGateNameCounts(snap, "by_entity", localByEntity, 0)
	mergeIDGateNameCounts(snap, "top_keys", localByKey, 10)
}

func mergeIDGateNameCounts(snap map[string]interface{}, field string, local map[string]int64, limit int) {
	if snap == nil || len(local) == 0 {
		return
	}
	merged := adminrollup.MergeInt64Maps(adminrollup.NameCountMapFromSnap(snap[field]), local)
	if len(merged) == 0 {
		return
	}
	snap[field] = topN(merged, limit)
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
