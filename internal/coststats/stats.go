// Package coststats provides an in-process rollup of LLM spend for the admin
// dashboard. Calendar-day buckets in UTC; optional Redis rollups via adminrollup.
package coststats

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/history"
)

// MaxRecentEvents bounds the recent-requests ring buffer.
const MaxRecentEvents = 50

type keySpend struct {
	KeyID          string  `json:"key_id,omitempty"`
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd"`
	OutputSpendUSD float64 `json:"output_spend_usd"`
	Requests       int64   `json:"requests"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
}

type providerSpend struct {
	Name           string  `json:"name"`
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd"`
	OutputSpendUSD float64 `json:"output_spend_usd"`
	Requests       int64   `json:"requests"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
}

type recentEntry struct {
	Time           int64   `json:"time"`
	Provider       string  `json:"provider"`
	KeyID          string  `json:"key_id,omitempty"`
	UserID         string  `json:"user_id,omitempty"`
	SpendUSD       float64 `json:"spend_usd"`
	InputSpendUSD  float64 `json:"input_spend_usd"`
	OutputSpendUSD float64 `json:"output_spend_usd"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	Model          string  `json:"model,omitempty"`
}

// Recorder accumulates rolling spend stats in-process.
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	spendTodayUSD       float64
	inputSpendTodayUSD  float64
	outputSpendTodayUSD float64
	requestsToday       int64
	inputTokensToday    int64
	outputTokensToday   int64

	byKey      map[string]*keySpend
	byProvider map[string]*providerSpend
	recent     []recentEntry
	flushed    costFlushed

	// Shared Redis rollup lifecycle (BindRollup/FlushRollup/QueueToday/
	// ArchiveDay/MergeHistory). Promoted methods satisfy the recorder's
	// public BindRollup/FlushRollup API.
	adminrollup.RecorderBinding
	history.Binding
}

type costFlushed struct {
	spendUSD, inputSpendUSD, outputSpendUSD float64
	requests, inputTokens, outputTokens     int64
	byProvider                              map[string]providerSpend
	byKey                                   map[string]keySpend
}

var costRollupCaps = adminrollup.TopNCaps{ByKey: 100}

// NewRecorder returns a ready-to-use Recorder scoped to the current UTC day.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt:  now,
		dayKey:     now.Format("2006-01-02"),
		byKey:      make(map[string]*keySpend),
		byProvider: make(map[string]*providerSpend),
	}
}

func (r *Recorder) maybeRollDay(now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if r.dayKey == day {
		return
	}
	oldDay := r.dayKey
	r.dayKey = day
	// Flush pending debounced deltas synchronously (persister mutex only — does
	// not take r.mu) so this instance's last old-day deltas land in Redis
	// before the archive goroutine snapshots. Only the archive runs async so a
	// UTC rollover never blocks concurrent RecordRequest on r.mu for seconds.
	r.FlushRollup()
	go func() {
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricCost, oldDay, costRollupCaps)
	}()
	r.flushed = costFlushed{}
	r.spendTodayUSD = 0
	r.inputSpendTodayUSD = 0
	r.outputSpendTodayUSD = 0
	r.requestsToday = 0
	r.inputTokensToday = 0
	r.outputTokensToday = 0
	r.byKey = make(map[string]*keySpend)
	r.byProvider = make(map[string]*providerSpend)
	r.recent = nil
}

func (r *Recorder) rollupDataLocked() map[string]interface{} {
	return map[string]interface{}{
		"spend_today_usd":        r.spendTodayUSD,
		"input_spend_today_usd":  r.inputSpendTodayUSD,
		"output_spend_today_usd": r.outputSpendTodayUSD,
		"requests_today":         r.requestsToday,
		"input_tokens_today":     r.inputTokensToday,
		"output_tokens_today":    r.outputTokensToday,
		"by_key":                 spendList(r.byKey),
		"by_provider":            providerList(r.byProvider),
	}
}

// RecordRequest ingests one tracked LLM request. keyID should be a masked iw:
// key when available (see middleware.MaskKeyID).
func (r *Recorder) RecordRequest(
	provider, keyID, userID, model string,
	spendUSD, inputSpendUSD, outputSpendUSD float64,
	inputTokens, outputTokens int,
) {
	if r == nil {
		return
	}

	now := time.Now().UTC()
	r.mu.Lock()

	r.maybeRollDay(now)

	r.spendTodayUSD += spendUSD
	r.inputSpendTodayUSD += inputSpendUSD
	r.outputSpendTodayUSD += outputSpendUSD
	r.requestsToday++
	r.inputTokensToday += int64(inputTokens)
	r.outputTokensToday += int64(outputTokens)

	if provider != "" {
		ps := r.byProvider[provider]
		if ps == nil {
			ps = &providerSpend{Name: provider}
			r.byProvider[provider] = ps
		}
		ps.SpendUSD += spendUSD
		ps.InputSpendUSD += inputSpendUSD
		ps.OutputSpendUSD += outputSpendUSD
		ps.Requests++
		ps.InputTokens += int64(inputTokens)
		ps.OutputTokens += int64(outputTokens)
	}

	scope := keyID
	if scope == "" {
		scope = userID
	}
	if scope != "" {
		ks := r.byKey[scope]
		if ks == nil {
			ks = &keySpend{KeyID: keyID}
			r.byKey[scope] = ks
		}
		ks.SpendUSD += spendUSD
		ks.InputSpendUSD += inputSpendUSD
		ks.OutputSpendUSD += outputSpendUSD
		ks.Requests++
		ks.InputTokens += int64(inputTokens)
		ks.OutputTokens += int64(outputTokens)
	}

	entry := recentEntry{
		Time:           now.Unix(),
		Provider:       provider,
		KeyID:          keyID,
		UserID:         userID,
		SpendUSD:       spendUSD,
		InputSpendUSD:  inputSpendUSD,
		OutputSpendUSD: outputSpendUSD,
		InputTokens:    inputTokens,
		OutputTokens:   outputTokens,
		Model:          model,
	}
	r.recent = append(r.recent, entry)
	if len(r.recent) > MaxRecentEvents {
		r.recent = r.recent[len(r.recent)-MaxRecentEvents:]
	}
	r.EmitHistory(entry)

	dayKey := r.dayKey
	delta := r.costDeltaLocked()
	r.advanceCostFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
	r.applyMonthlyKeySpendFromDelta(context.Background(), delta)
}

func (r *Recorder) applyMonthlyKeySpendFromDelta(ctx context.Context, delta adminrollup.Delta) {
	if r == nil {
		return
	}
	byKey := delta.Dimensions["by_key"]
	if len(byKey) == 0 {
		return
	}
	month := time.Now().UTC().Format("2006-01")
	for field, spendUSD := range byKey {
		member, f, ok := adminrollup.ParseDimMemberField(field)
		if !ok || f != "spend_usd" || spendUSD == 0 {
			continue
		}
		if err := r.ApplyFleetMonthlyKeySpend(ctx, adminrollup.MetricCost, month, member, spendUSD); err != nil {
			log.Printf("coststats: apply monthly key spend failed key=%s spend_usd=%f error=%v", member, spendUSD, err)
		}
	}
}

func (r *Recorder) costDeltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"spend_usd":        r.spendTodayUSD - r.flushed.spendUSD,
			"input_spend_usd":  r.inputSpendTodayUSD - r.flushed.inputSpendUSD,
			"output_spend_usd": r.outputSpendTodayUSD - r.flushed.outputSpendUSD,
			"requests":         float64(r.requestsToday - r.flushed.requests),
			"input_tokens":     float64(r.inputTokensToday - r.flushed.inputTokens),
			"output_tokens":    float64(r.outputTokensToday - r.flushed.outputTokens),
		},
		Dimensions: map[string]map[string]float64{
			"by_provider": {},
			"by_key":      {},
		},
	}
	for name, ps := range r.byProvider {
		prev := r.flushed.byProvider[name]
		addDim(d.Dimensions["by_provider"], name, "spend_usd", ps.SpendUSD-prev.SpendUSD)
		addDim(d.Dimensions["by_provider"], name, "input_spend_usd", ps.InputSpendUSD-prev.InputSpendUSD)
		addDim(d.Dimensions["by_provider"], name, "output_spend_usd", ps.OutputSpendUSD-prev.OutputSpendUSD)
		addDim(d.Dimensions["by_provider"], name, "requests", float64(ps.Requests-prev.Requests))
		addDim(d.Dimensions["by_provider"], name, "input_tokens", float64(ps.InputTokens-prev.InputTokens))
		addDim(d.Dimensions["by_provider"], name, "output_tokens", float64(ps.OutputTokens-prev.OutputTokens))
	}
	for scope, ks := range r.byKey {
		prev := r.flushed.byKey[scope]
		member := scope
		if ks.KeyID != "" {
			member = ks.KeyID
		}
		addDim(d.Dimensions["by_key"], member, "spend_usd", ks.SpendUSD-prev.SpendUSD)
		addDim(d.Dimensions["by_key"], member, "input_spend_usd", ks.InputSpendUSD-prev.InputSpendUSD)
		addDim(d.Dimensions["by_key"], member, "output_spend_usd", ks.OutputSpendUSD-prev.OutputSpendUSD)
		addDim(d.Dimensions["by_key"], member, "requests", float64(ks.Requests-prev.Requests))
		addDim(d.Dimensions["by_key"], member, "input_tokens", float64(ks.InputTokens-prev.InputTokens))
		addDim(d.Dimensions["by_key"], member, "output_tokens", float64(ks.OutputTokens-prev.OutputTokens))
	}
	return d
}

func addDim(m map[string]float64, member, field string, v float64) {
	if v == 0 {
		return
	}
	m[adminrollup.DimMemberField(member, field)] = v
}

func (r *Recorder) advanceCostFlushedLocked() {
	if r.flushed.byProvider == nil {
		r.flushed.byProvider = make(map[string]providerSpend)
	}
	if r.flushed.byKey == nil {
		r.flushed.byKey = make(map[string]keySpend)
	}
	r.flushed.spendUSD = r.spendTodayUSD
	r.flushed.inputSpendUSD = r.inputSpendTodayUSD
	r.flushed.outputSpendUSD = r.outputSpendTodayUSD
	r.flushed.requests = r.requestsToday
	r.flushed.inputTokens = r.inputTokensToday
	r.flushed.outputTokens = r.outputTokensToday
	for name, ps := range r.byProvider {
		r.flushed.byProvider[name] = *ps
	}
	for scope, ks := range r.byKey {
		r.flushed.byKey[scope] = *ks
	}
}

func spendList(m map[string]*keySpend) []keySpend {
	out := make([]keySpend, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpendUSD != out[j].SpendUSD {
			return out[i].SpendUSD > out[j].SpendUSD
		}
		return out[i].KeyID < out[j].KeyID
	})
	return out
}

func providerList(m map[string]*providerSpend) []providerSpend {
	out := make([]providerSpend, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SpendUSD != out[j].SpendUSD {
			return out[i].SpendUSD > out[j].SpendUSD
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Snapshot returns a JSON-serialisable view for the admin API.
func (r *Recorder) Snapshot() map[string]interface{} {
	if r == nil {
		return map[string]interface{}{"available": false}
	}

	r.mu.RLock()
	recent := make([]recentEntry, len(r.recent))
	for i, e := range r.recent {
		recent[len(r.recent)-1-i] = e
	}

	dayKey := r.dayKey
	snap := map[string]interface{}{
		"available":              true,
		"day":                    dayKey,
		"started_at":             r.startedAt.Unix(),
		"spend_today_usd":        r.spendTodayUSD,
		"input_spend_today_usd":  r.inputSpendTodayUSD,
		"output_spend_today_usd": r.outputSpendTodayUSD,
		"requests_today":         r.requestsToday,
		"input_tokens_today":     r.inputTokensToday,
		"output_tokens_today":    r.outputTokensToday,
		"by_key":                 spendList(r.byKey),
		"by_provider":            providerList(r.byProvider),
		"recent":                 recent,
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricCost, dayKey, snap, costRollupCaps)
	r.MergeHistory(adminrollup.MetricCost, snap)
	return snap
}

// KeySpendUSD returns recorded spend for a masked iw: key in the current UTC day.
//
// When a shared rollup store is bound (Redis), this returns the fleet-wide
// spend aggregated across every proxy instance, combined with this instance's
// in-process view as max(fleet, local). That combination is deliberately
// conservative for hard cluster-wide cost-limit enforcement:
//   - fleet captures spend recorded by OTHER instances (the in-process map
//     alone would let the effective cap scale with instance count);
//   - local never under-counts this instance's own just-recorded spend during
//     the brief window before its delta is flushed to Redis;
//   - max never exceeds the true fleet total (local is always a subset of the
//     fleet once flushed), so it cannot over-charge.
//
// If the recorder's bucket is for a prior UTC day, the local contribution is
// treated as 0 rather than stale spend. That matters because a cost-limited
// key's blocked (402) requests are never cost-tracked, so RecordRequest — the
// only caller of maybeRollDay — may not fire after midnight. Without this guard
// a key that hit its cap at 23:59 would stay blocked forever into the new day.
func (r *Recorder) KeySpendUSD(ctx context.Context, keyID string) float64 {
	spend, _ := r.KeySpendUSDDetailed(ctx, keyID)
	return spend
}

// ReserveKeySpend atomically reserves an estimated cost for keyID against its
// daily cap across the fleet. Returns (allowed, reservationActive):
//   - reservationActive=false: no rollup store is bound, so no fleet
//     reservation was made. Callers should fall back to read-only enforcement
//     (KeySpendUSD) for this request; allowed is meaningless.
//   - reservationActive=true, allowed=true: reserved; request may proceed and
//     the caller MUST later release/reconcile via AdjustKeyReservation.
//   - reservationActive=true, allowed=false: the combined recorded + reserved
//     spend has reached the cap; the request should be blocked (nothing was
//     reserved, so no release is owed).
//
// On a backing-store error the reservation is treated as inactive (the caller
// degrades to read-only enforcement) so a Redis blip never hard-fails traffic.
func (r *Recorder) ReserveKeySpend(ctx context.Context, keyID string, estimateUSD float64, limitCents int64) (allowed, reservationActive bool) {
	if r == nil || keyID == "" || limitCents <= 0 {
		return true, false
	}
	today := time.Now().UTC().Format("2006-01-02")
	allowed, bound, err := r.ReserveFleetKeySpend(ctx, adminrollup.MetricCost, today, keyID, estimateUSD, limitCents)
	if !bound || err != nil {
		return true, false
	}
	return allowed, true
}

// AdjustKeyReservation changes keyID's outstanding fleet reservation by
// deltaUSD (negative to release). No-op when no rollup store is bound.
func (r *Recorder) AdjustKeyReservation(ctx context.Context, keyID string, deltaUSD float64) {
	if r == nil || keyID == "" || deltaUSD == 0 {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	if err := r.AdjustFleetKeyReservation(ctx, adminrollup.MetricCost, today, keyID, deltaUSD); err != nil {
		log.Printf("coststats: adjust reservation failed key=%s delta_usd=%f error=%v", keyID, deltaUSD, err)
	}
}

// KeySpendUSDDetailed is KeySpendUSD plus a degraded flag. degraded is true
// only when a fleet rollup store IS bound but its read failed: the returned
// spend is then this instance's local-only view, which cannot see spend
// recorded by sibling instances. Callers enforcing hard cluster-wide caps can
// use this to fail closed instead of silently weakening to per-instance
// enforcement (the failure mode that lets a fleet overshoot its cap when
// Redis is unreachable). When unbound (local-only by design) degraded is
// false: there are no siblings to miss.
func (r *Recorder) KeySpendUSDDetailed(ctx context.Context, keyID string) (spendUSD float64, degraded bool) {
	if r == nil || keyID == "" {
		return 0, false
	}
	today := time.Now().UTC().Format("2006-01-02")

	r.mu.RLock()
	var local float64
	if r.dayKey == today {
		if ks := r.byKey[keyID]; ks != nil {
			local = ks.SpendUSD
		}
	}
	r.mu.RUnlock()

	fleet, bound, err := r.FleetKeySpendUSD(ctx, adminrollup.MetricCost, today, keyID)
	if bound && err != nil {
		return local, true
	}
	if bound && fleet > local {
		return fleet, false
	}
	return local, false
}

// KeyMonthlySpendUSD returns recorded monthly spend for a masked iw: key.
func (r *Recorder) KeyMonthlySpendUSD(ctx context.Context, keyID string) float64 {
	spend, _ := r.KeyMonthlySpendUSDDetailed(ctx, keyID)
	return spend
}

// ReserveKeyMonthlySpend atomically reserves an estimated cost for keyID against
// its monthly cap across the fleet. See ReserveKeySpend for the return contract.
func (r *Recorder) ReserveKeyMonthlySpend(ctx context.Context, keyID string, estimateUSD float64, limitCents int64) (allowed, reservationActive bool) {
	if r == nil || keyID == "" || limitCents <= 0 {
		return true, false
	}
	month := time.Now().UTC().Format("2006-01")
	allowed, bound, err := r.ReserveFleetKeyMonthlySpend(ctx, adminrollup.MetricCost, month, keyID, estimateUSD, limitCents)
	if !bound || err != nil {
		return true, false
	}
	return allowed, true
}

// AdjustKeyMonthlyReservation changes keyID's outstanding monthly fleet
// reservation by deltaUSD (negative to release).
func (r *Recorder) AdjustKeyMonthlyReservation(ctx context.Context, keyID string, deltaUSD float64) {
	if r == nil || keyID == "" || deltaUSD == 0 {
		return
	}
	month := time.Now().UTC().Format("2006-01")
	if err := r.AdjustFleetKeyMonthlyReservation(ctx, adminrollup.MetricCost, month, keyID, deltaUSD); err != nil {
		log.Printf("coststats: adjust monthly reservation failed key=%s delta_usd=%f error=%v", keyID, deltaUSD, err)
	}
}

// KeyMonthlySpendUSDDetailed is KeyMonthlySpendUSD plus a degraded flag.
func (r *Recorder) KeyMonthlySpendUSDDetailed(ctx context.Context, keyID string) (spendUSD float64, degraded bool) {
	if r == nil || keyID == "" {
		return 0, false
	}
	month := time.Now().UTC().Format("2006-01")

	fleet, bound, err := r.FleetKeyMonthlySpendUSD(ctx, adminrollup.MetricCost, month, keyID)
	if bound && err != nil {
		return 0, true
	}
	if bound {
		return fleet, false
	}
	return 0, false
}
