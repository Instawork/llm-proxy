// Package ratelimitstats provides in-process rate-limit decision stats for the
// admin dashboard, with optional Redis rollups and row history.
package ratelimitstats

import (
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/history"
)

// MaxRecentBlocks bounds the recent blocked-events ring buffer.
const MaxRecentBlocks = 50

type blockEvent struct {
	Time      int64  `json:"time"`
	Provider  string `json:"provider"`
	Model     string `json:"model,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Metric    string `json:"metric,omitempty"`
	Window    string `json:"window,omitempty"`
	ScopeKey  string `json:"scope_key,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Remaining int    `json:"remaining,omitempty"`
}

type rateLimitFlushed struct {
	requestsTotal, requestsAllowed, requestsBlocked int64
	byProvider, byReason                            map[string]int64
}

// Recorder accumulates rolling rate-limit stats in-process.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	requestsTotal   int64
	requestsAllowed int64
	requestsBlocked int64

	byProvider map[string]int64
	byReason   map[string]int64

	recentBlocks []blockEvent
	flushed      rateLimitFlushed

	adminrollup.RecorderBinding
	history.Binding
}

// NewRecorder returns a recorder scoped to the current UTC day.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		byProvider: make(map[string]int64),
		byReason:   make(map[string]int64),
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricRateLimit, oldDay, adminrollup.TopNCaps{})
	}()
	r.flushed = rateLimitFlushed{}
	r.requestsTotal = 0
	r.requestsAllowed = 0
	r.requestsBlocked = 0
	r.byProvider = make(map[string]int64)
	r.byReason = make(map[string]int64)
	r.recentBlocks = nil
}

func int64MapDelta(cur, prev map[string]int64) map[string]float64 {
	out := make(map[string]float64)
	for k, v := range cur {
		if dr := float64(v - prev[k]); dr != 0 {
			out[k] = dr
		}
	}
	return out
}

func (r *Recorder) deltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"requests_total":   float64(r.requestsTotal - r.flushed.requestsTotal),
			"requests_allowed": float64(r.requestsAllowed - r.flushed.requestsAllowed),
			"requests_blocked": float64(r.requestsBlocked - r.flushed.requestsBlocked),
		},
	}
	if provDelta := int64MapDelta(r.byProvider, r.flushed.byProvider); len(provDelta) > 0 {
		if d.Dimensions == nil {
			d.Dimensions = make(map[string]map[string]float64)
		}
		d.Dimensions["by_provider"] = provDelta
	}
	if reasonDelta := int64MapDelta(r.byReason, r.flushed.byReason); len(reasonDelta) > 0 {
		if d.Dimensions == nil {
			d.Dimensions = make(map[string]map[string]float64)
		}
		d.Dimensions["by_reason"] = reasonDelta
	}
	return d
}

func (r *Recorder) advanceFlushedLocked() {
	if r.flushed.byProvider == nil {
		r.flushed.byProvider = make(map[string]int64)
	}
	if r.flushed.byReason == nil {
		r.flushed.byReason = make(map[string]int64)
	}
	r.flushed.requestsTotal = r.requestsTotal
	r.flushed.requestsAllowed = r.requestsAllowed
	r.flushed.requestsBlocked = r.requestsBlocked
	for k, v := range r.byProvider {
		r.flushed.byProvider[k] = v
	}
	for k, v := range r.byReason {
		r.flushed.byReason[k] = v
	}
}

// RecordDecision ingests one rate-limit check. Memory and Redis aggregates
// cover every decision; row history archives blocked requests only.
func (r *Recorder) RecordDecision(
	provider, model, keyID, userID string,
	allowed bool,
	reason, metric, window, scopeKey string,
	limit, remaining int,
) {
	if r == nil {
		return
	}
	now := time.Now().UTC()
	r.mu.Lock()
	r.maybeRollDay(now)
	r.requestsTotal++
	if allowed {
		r.requestsAllowed++
	} else {
		r.requestsBlocked++
		if provider != "" {
			r.byProvider[provider]++
		}
		if reason != "" {
			r.byReason[reason]++
		}
		entry := blockEvent{
			Time:      now.Unix(),
			Provider:  provider,
			Model:     model,
			KeyID:     keyID,
			UserID:    userID,
			Reason:    reason,
			Metric:    metric,
			Window:    window,
			ScopeKey:  scopeKey,
			Limit:     limit,
			Remaining: remaining,
		}
		r.recentBlocks = append(r.recentBlocks, entry)
		if len(r.recentBlocks) > MaxRecentBlocks {
			r.recentBlocks = r.recentBlocks[len(r.recentBlocks)-MaxRecentBlocks:]
		}
		r.EmitHistory(entry)
	}
	dayKey := r.dayKey
	delta := r.deltaLocked()
	r.advanceFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
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

	var recent []blockEvent
	if localActive {
		recent = make([]blockEvent, len(r.recentBlocks))
		for i, e := range r.recentBlocks {
			recent[len(r.recentBlocks)-1-i] = e
		}
	}

	var requestsTotal, requestsAllowed, requestsBlocked int64
	byProvider := make(map[string]int64)
	byReason := make(map[string]int64)
	if localActive {
		requestsTotal = r.requestsTotal
		requestsAllowed = r.requestsAllowed
		requestsBlocked = r.requestsBlocked
		for k, v := range r.byProvider {
			byProvider[k] = v
		}
		for k, v := range r.byReason {
			byReason[k] = v
		}
	}

	snap := map[string]interface{}{
		"available":        true,
		"day":              today,
		"started_at":       startedAt.Unix(),
		"requests_total":   requestsTotal,
		"requests_allowed": requestsAllowed,
		"requests_blocked": requestsBlocked,
		"by_provider":      byProvider,
		"by_reason":        byReason,
		"recent_blocks":    recent,
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricRateLimit, today, snap, adminrollup.TopNCaps{})
	if localActive {
		mergeLocalRateLimitIntoSnap(snap, requestsTotal, requestsAllowed, requestsBlocked, byProvider, byReason)
	}
	r.MergeHistory(adminrollup.MetricRateLimit, snap)
	r.MergeHourly(adminrollup.MetricRateLimit, snap)
	return snap
}

func mergeLocalRateLimitIntoSnap(
	snap map[string]interface{},
	requestsTotal, requestsAllowed, requestsBlocked int64,
	byProvider, byReason map[string]int64,
) {
	adminrollup.MergeSnapInt64Max(snap, "requests_total", requestsTotal)
	adminrollup.MergeSnapInt64Max(snap, "requests_allowed", requestsAllowed)
	adminrollup.MergeSnapInt64Max(snap, "requests_blocked", requestsBlocked)
	adminrollup.MergeSnapInt64Map(snap, "by_provider", byProvider)
	adminrollup.MergeSnapInt64Map(snap, "by_reason", byReason)
}
