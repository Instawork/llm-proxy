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
	r.ArchiveDay(r.dayKey, r.rollupDataLocked())
	r.dayKey = day
	r.requestsTotal = 0
	r.requestsAllowed = 0
	r.requestsBlocked = 0
	r.byProvider = make(map[string]int64)
	r.byReason = make(map[string]int64)
	r.recentBlocks = nil
}

func (r *Recorder) rollupDataLocked() map[string]interface{} {
	return map[string]interface{}{
		"requests_total":   r.requestsTotal,
		"requests_allowed": r.requestsAllowed,
		"requests_blocked": r.requestsBlocked,
		"by_provider":      r.byProvider,
		"by_reason":        r.byReason,
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
	rollup := r.rollupDataLocked()
	r.mu.Unlock()

	r.QueueToday(dayKey, rollup)
}

// Snapshot returns JSON for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}
	r.mu.RLock()
	recent := make([]blockEvent, len(r.recentBlocks))
	for i, e := range r.recentBlocks {
		recent[len(r.recentBlocks)-1-i] = e
	}
	snap := map[string]interface{}{
		"available":        true,
		"day":              r.dayKey,
		"started_at":       r.startedAt.Unix(),
		"requests_total":   r.requestsTotal,
		"requests_allowed": r.requestsAllowed,
		"requests_blocked": r.requestsBlocked,
		"by_provider":      r.byProvider,
		"by_reason":        r.byReason,
		"recent_blocks":    recent,
	}
	r.mu.RUnlock()

	r.MergeHistory(adminrollup.MetricRateLimit, snap)
	return snap
}
