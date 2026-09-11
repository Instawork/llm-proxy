// Package unmeteredstats counts proxied provider requests that produced no
// token usage and therefore no cost record (embeddings, model listings, failed
// calls), so dashboards can explain traffic that never shows up as spend.
package unmeteredstats

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// Label cardinality bounds; labels beyond them fold into "other".
const (
	maxEndpointsPerKey = 25
	maxEndpoints       = 200
)

const otherEndpoint = "other"

const maxLabelLen = 120

type flushed struct {
	total      int64
	byEndpoint map[string]int64
	byKey      map[string]int64
}

// Recorder accumulates unmetered request counts in-process and publishes
// deltas to admin rollups for fleet-wide visibility.
type Recorder struct {
	mu     sync.RWMutex
	dayKey string

	total      int64
	byEndpoint map[string]int64
	// byKey is keyed "<key_id>|<endpoint>" to match the rollup hash field.
	byKey map[string]int64

	flushed flushed

	adminrollup.RecorderBinding
}

// NewRecorder returns a ready-to-use recorder.
func NewRecorder() *Recorder {
	return &Recorder{
		dayKey:     time.Now().UTC().Format("2006-01-02"),
		byEndpoint: make(map[string]int64),
		byKey:      make(map[string]int64),
	}
}

// EndpointLabel turns a proxied request path into a bounded label using the
// shared endpoint template, and adds the HTTP status to failed responses so a
// rejected chat call is not confused with a metered one.
func EndpointLabel(path string, status int) string {
	label := providers.EndpointTemplate(path)
	if len(label) > maxLabelLen {
		label = label[:maxLabelLen]
	}
	if status >= 400 {
		label = fmt.Sprintf("%s (HTTP %d)", label, status)
	}
	return label
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricUnmetered, oldDay, adminrollup.TopNCaps{})
	}()
	r.flushed = flushed{}
	r.total = 0
	r.byEndpoint = make(map[string]int64)
	r.byKey = make(map[string]int64)
}

func (r *Recorder) deltaLocked() adminrollup.Delta {
	return adminrollup.Delta{
		Totals: map[string]float64{"requests": float64(r.total - r.flushed.total)},
		Dimensions: map[string]map[string]float64{
			"by_endpoint": intMapDelta(r.byEndpoint, r.flushed.byEndpoint),
			"by_key":      intMapDelta(r.byKey, r.flushed.byKey),
		},
	}
}

func (r *Recorder) publishLocked() {
	dayKey := r.dayKey
	delta := r.deltaLocked()
	r.flushed = flushed{total: r.total, byEndpoint: copyIntMap(r.byEndpoint), byKey: copyIntMap(r.byKey)}
	r.mu.Unlock()
	r.QueueDelta(dayKey, delta)
	r.mu.Lock()
}

func boundedEndpoint(seen map[string]int64, endpoint string, limit int) string {
	if _, ok := seen[endpoint]; ok || len(seen) < limit {
		return endpoint
	}
	return otherEndpoint
}

// keyEndpointLocked returns the endpoint to count under for keyID, folding
// into "other" once the key already tracks maxEndpointsPerKey labels.
func (r *Recorder) keyEndpointLocked(keyID, endpoint string) string {
	if _, seen := r.byKey[adminrollup.DimMemberField(keyID, endpoint)]; seen {
		return endpoint
	}
	prefix := keyID + "|"
	n := 0
	for k := range r.byKey {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	if n >= maxEndpointsPerKey {
		return otherEndpoint
	}
	return endpoint
}

// RecordRequest counts one unmetered request. keyID is the masked proxy key
// (empty when the request carried none); endpoint should come from EndpointLabel.
func (r *Recorder) RecordRequest(keyID, endpoint string) {
	if r == nil || endpoint == "" {
		return
	}
	r.mu.Lock()
	r.maybeRollDay(time.Now())
	r.total++
	r.byEndpoint[boundedEndpoint(r.byEndpoint, endpoint, maxEndpoints)]++
	if keyID != "" {
		r.byKey[adminrollup.DimMemberField(keyID, r.keyEndpointLocked(keyID, endpoint))]++
	}
	r.publishLocked()
	r.mu.Unlock()
}

// Snapshot returns a JSON-serialisable view for the admin API:
// requests_today, by_endpoint ([{name,count}]) and by_key (key → endpoint → count).
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}
	today := time.Now().UTC().Format("2006-01-02")

	r.mu.RLock()
	localActive := r.dayKey == today
	var total int64
	var localByEndpoint map[string]int64
	localByKey := map[string]map[string]int64{}
	if localActive {
		total = r.total
		localByEndpoint = copyIntMap(r.byEndpoint)
		localByKey = nestByKey(r.byKey)
	}
	r.mu.RUnlock()

	backend := "memory"
	if r.RollupBound() {
		backend = "redis"
	}
	snap := map[string]interface{}{
		"available":      true,
		"backend":        backend,
		"day":            today,
		"requests_today": int64(0),
		"by_endpoint":    []map[string]interface{}{},
		"by_key":         map[string]map[string]int64{},
	}
	r.MergeToday(adminrollup.MetricUnmetered, today, snap, adminrollup.TopNCaps{})
	if localActive {
		adminrollup.MergeSnapInt64Max(snap, "requests_today", total)
		adminrollup.MergeSnapNameCounts(snap, "by_endpoint", localByEndpoint, 0)
		snap["by_key"] = mergeByKey(byKeyFromSnap(snap["by_key"]), localByKey)
	}
	return snap
}

func nestByKey(flat map[string]int64) map[string]map[string]int64 {
	out := make(map[string]map[string]int64)
	for k, v := range flat {
		keyID, endpoint, ok := adminrollup.ParseDimMemberField(k)
		if !ok {
			continue
		}
		if out[keyID] == nil {
			out[keyID] = make(map[string]int64)
		}
		out[keyID][endpoint] = v
	}
	return out
}

func byKeyFromSnap(raw interface{}) map[string]map[string]int64 {
	if m, ok := raw.(map[string]map[string]int64); ok {
		return m
	}
	return nil
}

// mergeByKey returns per key/endpoint max(a, b), the same "highest observer
// wins" rule the cost snapshot uses for memory-vs-Redis overlap.
func mergeByKey(a, b map[string]map[string]int64) map[string]map[string]int64 {
	out := make(map[string]map[string]int64, len(a)+len(b))
	for k, v := range a {
		out[k] = copyIntMap(v)
	}
	for k, v := range b {
		out[k] = adminrollup.MergeInt64Maps(out[k], v)
	}
	return out
}
