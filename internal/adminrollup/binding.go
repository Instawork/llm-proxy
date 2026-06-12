package adminrollup

import (
	"context"
	"sync"
	"time"
)

// mergeHistoryTimeout bounds the Redis read+merge done on each live Snapshot so
// a slow rollup store can never stall an admin API request.
const mergeHistoryTimeout = 2 * time.Second

// archiveTimeout bounds the Redis read+write+delete done when archiving a
// completed UTC day's hash aggregates. It is larger than mergeHistoryTimeout
// because the archive does more work (build today data, write the daily JSON,
// delete the per-dimension hashes) and runs off the request path on rollover.
const archiveTimeout = 5 * time.Second

// RecorderBinding is an embeddable helper that gives metric recorders
// (coststats, usagestats, pii) the shared Redis rollup lifecycle: attach a
// store/persister once at startup, queue today's snapshot, archive a completed
// day, merge daily history into a live snapshot, and flush on shutdown.
//
// Each recorder keeps its own metric name and metric-specific aggregation;
// this owns only the store/persister plumbing so lifecycle details (e.g. the
// MergeHistory timeout) live in exactly one place. The zero value is ready to
// use and behaves as a no-op until BindRollup is called.
type RecorderBinding struct {
	mu        sync.RWMutex
	store     *Store
	persister *Persister
}

// BindRollup attaches the rollup store and persister. Intended to be called
// once at startup before traffic. Safe for concurrent use.
func (b *RecorderBinding) BindRollup(store *Store, persister *Persister) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.store = store
	b.persister = persister
	b.mu.Unlock()
}

func (b *RecorderBinding) deps() (*Store, *Persister) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.store, b.persister
}

// QueueToday schedules a debounced write of today's snapshot (no-op if unbound).
func (b *RecorderBinding) QueueToday(day string, data map[string]interface{}) {
	if _, p := b.deps(); p != nil {
		p.QueueToday(day, data)
	}
}

// QueueDelta schedules a debounced atomic merge of today's delta (no-op if unbound).
func (b *RecorderBinding) QueueDelta(day string, d Delta) {
	if _, p := b.deps(); p != nil {
		p.QueueDelta(day, d)
	}
}

// ArchiveDay writes a completed day's data to the daily key immediately
// (no-op if unbound). Used on UTC day rollover.
func (b *RecorderBinding) ArchiveDay(day string, data map[string]interface{}) {
	if _, p := b.deps(); p != nil {
		p.ArchiveImmediately(day, data)
	}
}

// ArchiveDayFromAggregates archives hash-backed today data (no-op if unbound).
func (b *RecorderBinding) ArchiveDayFromAggregates(metric, day string, caps TopNCaps) {
	s, _ := b.deps()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	if err := s.ArchiveDailyFromAggregates(ctx, metric, day, caps); err != nil {
		s.logger.Warn("admin rollup: archive from aggregates failed", "metric", metric, "day", day, "error", err)
	}
}

// ArchiveDayFromAggregatesElected archives hash-backed today data, but only the
// instance that wins the per-day archiver election performs the archive+delete.
// This prevents multiple sidecars from racing the same archive (and the
// delete-then-recreate hazard where one writer removes today's hashes while
// another is still flushing a debounced delta into them). Callers should still
// FlushRollup() first so their own pending deltas land before the winner
// snapshots the completed day. No-op if unbound.
func (b *RecorderBinding) ArchiveDayFromAggregatesElected(metric, day string, caps TopNCaps) {
	s, _ := b.deps()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	if !s.TryElectArchiver(ctx, metric, day, archiverHolderID()) {
		return
	}
	if err := s.ArchiveDailyFromAggregates(ctx, metric, day, caps); err != nil {
		s.logger.Warn("admin rollup: elected archive failed", "metric", metric, "day", day, "error", err)
	}
}

// MergeToday overlays fleet-wide today totals from Redis (no-op if unbound).
func (b *RecorderBinding) MergeToday(metric, day string, snap map[string]interface{}, caps TopNCaps) {
	s, _ := b.deps()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mergeHistoryTimeout)
	defer cancel()
	s.MergeToday(ctx, metric, day, snap, caps)
}

// MergeHistory folds persisted daily history into a live snapshot under a
// bounded timeout (no-op if unbound).
func (b *RecorderBinding) MergeHistory(metric string, snap map[string]interface{}) {
	s, _ := b.deps()
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), mergeHistoryTimeout)
	defer cancel()
	s.MergeHistory(ctx, metric, snap)
}

// FlushRollup forces pending Redis writes (for shutdown; no-op if unbound).
func (b *RecorderBinding) FlushRollup() {
	if _, p := b.deps(); p != nil {
		p.FlushNow()
	}
}
