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

// RollupBound reports whether a rollup store is attached (i.e. fleet-wide
// reads via Redis are available rather than only the in-process view).
func (b *RecorderBinding) RollupBound() bool {
	s, _ := b.deps()
	return s != nil
}

// FleetKeySpendUSD returns the fleet-wide spend (USD) recorded across all
// instances for keyID on the given metric/day, read straight from the shared
// rollup store. The read is bounded by mergeHistoryTimeout (and the caller's
// ctx) so a slow store never stalls a request-path caller.
//
// Return contract (three distinct states callers MUST treat differently):
//   - (0,   false, nil): unbound — no rollup store. Fleet enforcement was
//     never expected; falling back to the in-process view is correct.
//   - (v,   true,  nil): bound and read succeeded; v is the fleet total.
//   - (0,   true,  err): bound but the Redis read FAILED. The caller cannot
//     see other instances' spend right now; this is a degraded state, not a
//     "spend is zero" state. Callers that enforce hard cluster-wide limits
//     should treat this as untrusted (e.g. fail closed) rather than silently
//     dropping to per-instance enforcement.
func (b *RecorderBinding) FleetKeySpendUSD(ctx context.Context, metric, day, keyID string) (float64, bool, error) {
	s, _ := b.deps()
	if s == nil {
		return 0, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	v, err := s.KeySpendUSD(ctx, metric, day, keyID)
	if err != nil {
		return 0, true, err
	}
	return v, true, nil
}

// ReserveFleetKeySpend atomically reserves estimateUSD for keyID against its
// daily cap across the fleet. The bool reports whether a rollup store is bound
// (when false, the caller has no fleet reservation and should fall back to its
// read-only enforcement path). allowed is meaningful only when bound: true =>
// reserved and may proceed, false => cap reached, block. The read+write is
// bounded by mergeHistoryTimeout (and ctx).
func (b *RecorderBinding) ReserveFleetKeySpend(ctx context.Context, metric, day, keyID string, estimateUSD float64, limitCents int64) (allowed, bound bool, err error) {
	s, _ := b.deps()
	if s == nil {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	allowed, err = s.ReserveKeySpend(ctx, metric, day, keyID, estimateUSD, limitCents)
	return allowed, true, err
}

// AdjustFleetKeyReservation adjusts keyID's outstanding reservation by deltaUSD
// (negative to release). No-op when unbound. Bounded by mergeHistoryTimeout.
func (b *RecorderBinding) AdjustFleetKeyReservation(ctx context.Context, metric, day, keyID string, deltaUSD float64) error {
	s, _ := b.deps()
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	return s.AddKeyReservation(ctx, metric, day, keyID, deltaUSD)
}

// FleetKeyMonthlySpendUSD returns the fleet-wide monthly spend (USD) recorded
// across all instances for keyID on the given metric/month.
func (b *RecorderBinding) FleetKeyMonthlySpendUSD(ctx context.Context, metric, month, keyID string) (float64, bool, error) {
	s, _ := b.deps()
	if s == nil {
		return 0, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	v, err := s.KeyMonthlySpendUSD(ctx, metric, month, keyID)
	if err != nil {
		return 0, true, err
	}
	return v, true, nil
}

// ApplyFleetMonthlyKeySpend atomically folds spendUSD into the monthly per-key
// hash. No-op when unbound.
func (b *RecorderBinding) ApplyFleetMonthlyKeySpend(ctx context.Context, metric, month, keyID string, spendUSD float64) error {
	s, _ := b.deps()
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	return s.ApplyMonthlyKeySpend(ctx, metric, month, keyID, spendUSD)
}

// ReserveFleetKeyMonthlySpend atomically reserves estimateUSD for keyID against
// its monthly cap across the fleet.
func (b *RecorderBinding) ReserveFleetKeyMonthlySpend(ctx context.Context, metric, month, keyID string, estimateUSD float64, limitCents int64) (allowed, bound bool, err error) {
	s, _ := b.deps()
	if s == nil {
		return false, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	allowed, err = s.ReserveKeyMonthlySpend(ctx, metric, month, keyID, estimateUSD, limitCents)
	return allowed, true, err
}

// AdjustFleetKeyMonthlyReservation adjusts keyID's outstanding monthly
// reservation by deltaUSD (negative to release). No-op when unbound.
func (b *RecorderBinding) AdjustFleetKeyMonthlyReservation(ctx context.Context, metric, month, keyID string, deltaUSD float64) error {
	s, _ := b.deps()
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, mergeHistoryTimeout)
	defer cancel()
	return s.AddKeyMonthlyReservation(ctx, metric, month, keyID, deltaUSD)
}

// FlushRollup forces pending Redis writes (for shutdown; no-op if unbound).
func (b *RecorderBinding) FlushRollup() {
	if _, p := b.deps(); p != nil {
		p.FlushNow()
	}
}
