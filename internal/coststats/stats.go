// Package coststats provides an in-process rollup of LLM spend for the admin
// dashboard. Calendar-day buckets in UTC; optional Redis rollups via adminrollup.
package coststats

import (
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
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

	dayKey := r.dayKey
	delta := r.costDeltaLocked()
	r.advanceCostFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
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
