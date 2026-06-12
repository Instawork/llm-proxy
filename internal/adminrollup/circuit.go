package adminrollup

import (
	"context"
	"os"
	"time"

	"github.com/Instawork/llm-proxy/internal/circuit"
)

// CircuitProviderDay captures the per-UTC-day PEAK circuit stats for one
// provider. The circuit store reports failures within a short rolling window
// (WindowSeconds), so any single snapshot is a point-in-time gauge — not a
// cumulative or per-day total. We therefore archive the worst (peak) window
// failure count observed during the day, which is a stable, comparable daily
// statistic that the frontend can render per-day or peak-over-range without
// double-counting.
type CircuitProviderDay struct {
	Failures   int    `json:"failures"`
	RollupOpen bool   `json:"rollup_open,omitempty"`
	State      string `json:"state,omitempty"`
}

// circuitDayPeak folds successive circuit snapshots into the running per-day
// peak (element-wise max of the windowed failure gauge per provider).
type circuitDayPeak struct {
	providers map[string]CircuitProviderDay
}

func newCircuitDayPeak() *circuitDayPeak {
	return &circuitDayPeak{providers: map[string]CircuitProviderDay{}}
}

// merge folds a fresh SnapshotCircuit map into the running daily peak.
func (p *circuitDayPeak) merge(snap map[string]interface{}) {
	provs, ok := snap["providers"].(map[string]interface{})
	if !ok {
		return
	}
	for name, raw := range provs {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		cur := p.providers[name]
		if f := snapInt(entry["failures"]); f > cur.Failures {
			cur.Failures = f
		}
		if open, ok := entry["rollup_open"].(bool); ok && open {
			cur.RollupOpen = true
		}
		if state, ok := entry["state"].(string); ok && state != "" {
			cur.State = state // last observed state for the day
		}
		p.providers[name] = cur
	}
}

// payload renders the accumulated peak as a JSON-ready map matching the
// SnapshotCircuit shape the daily_history consumers expect.
func (p *circuitDayPeak) payload() map[string]interface{} {
	total := 0
	provOut := make(map[string]interface{}, len(p.providers))
	for name, d := range p.providers {
		total += d.Failures
		provOut[name] = map[string]interface{}{
			"failures":    d.Failures,
			"state":       d.State,
			"rollup_open": d.RollupOpen,
		}
	}
	return map[string]interface{}{
		"total_failures": total,
		"providers":      provOut,
	}
}

// snapInt coerces a snapshot numeric field to int. Values built in-process are
// int; values that round-tripped through JSON/Redis are float64.
func snapInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

// SnapshotCircuit reads current circuit stats and returns a JSON-ready map.
func SnapshotCircuit(
	ctx context.Context,
	store circuit.Store,
	providers []string,
	rollupThreshold int,
	rollupWindowSec int,
) map[string]interface{} {
	if store == nil {
		return map[string]interface{}{"total_failures": 0, "providers": map[string]interface{}{}}
	}
	providerOut := make(map[string]interface{}, len(providers))
	total := 0
	for _, name := range providers {
		stats, err := store.GetStats(ctx, name)
		if err != nil {
			continue
		}
		total += stats.Failures
		entry := map[string]interface{}{
			"failures": stats.Failures,
			"state":    stats.State.String(),
		}
		if rec, ok := store.(circuit.RollupRecorder); ok && rollupThreshold > 0 {
			open, count, _ := rec.RollupOpen(ctx, name, rollupThreshold, rollupWindowSec)
			entry["rollup_open"] = open
			entry["rollup_count"] = count
		}
		providerOut[name] = entry
	}
	return map[string]interface{}{
		"total_failures": total,
		"providers":      providerOut,
	}
}

// RunCircuitArchiver snapshots circuit stats on UTC day change.
func RunCircuitArchiver(
	store *Store,
	persister *Persister,
	cbStore circuit.Store,
	providers []string,
	rollupThreshold int,
	rollupWindowSec int,
	stop <-chan struct{},
) {
	if store == nil || persister == nil || cbStore == nil {
		return
	}
	lastDay := time.Now().UTC().Format("2006-01-02")
	// peak holds the running per-provider peak for the current UTC day. On a
	// mid-day process restart it starts fresh (today's already-persisted peak
	// is not reloaded), so today's value can briefly dip until the gauge
	// climbs again — acceptable for best-effort observability.
	peak := newCircuitDayPeak()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			now := time.Now().UTC()
			day := now.Format("2006-01-02")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if !store.TryElectArchiver(ctx, MetricCircuit, day, archiverHolderID()) {
				cancel()
				continue
			}
			snap := SnapshotCircuit(ctx, cbStore, providers, rollupThreshold, rollupWindowSec)
			cancel()

			if day != lastDay {
				// Archive the completed day's accumulated peak, then reset for
				// the new day so per-day values never carry over.
				persister.ArchiveImmediately(lastDay, peak.payload())
				peak = newCircuitDayPeak()
				lastDay = day
			}

			peak.merge(snap)
			persister.QueueToday(day, peak.payload())
		}
	}
}

func archiverHolderID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "llm-proxy"
}
