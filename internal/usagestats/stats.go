// Package usagestats tracks request/token volume for the admin Usage page.
package usagestats

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/history"
)

type scopeUsage struct {
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// Recorder accumulates daily usage by scope (global, model, provider, key, user).
type Recorder struct {
	mu        sync.RWMutex
	startedAt time.Time
	dayKey    string

	global  scopeUsage
	byModel map[string]*scopeUsage
	byProv  map[string]*scopeUsage
	byKey   map[string]*scopeUsage
	byUser  map[string]*scopeUsage
	flushed usageFlushed

	// Shared Redis rollup lifecycle; promoted methods satisfy the recorder's
	// public BindRollup/FlushRollup API.
	adminrollup.RecorderBinding
	history.Binding
}

type usageEvent struct {
	Time         int64  `json:"time"`
	Provider     string `json:"provider"`
	Model        string `json:"model,omitempty"`
	KeyID        string `json:"key_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
}

type usageFlushed struct {
	global  scopeUsage
	byModel map[string]scopeUsage
	byProv  map[string]scopeUsage
	byKey   map[string]scopeUsage
	byUser  map[string]scopeUsage
}

var usageRollupCaps = adminrollup.TopNCaps{ByKey: 100, ByUser: 100}

// NewRecorder returns a recorder for the current UTC day.
func NewRecorder() *Recorder {
	now := time.Now().UTC()
	return &Recorder{
		startedAt: now,
		dayKey:    now.Format("2006-01-02"),
		byModel:   make(map[string]*scopeUsage),
		byProv:    make(map[string]*scopeUsage),
		byKey:     make(map[string]*scopeUsage),
		byUser:    make(map[string]*scopeUsage),
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
		r.ArchiveDayFromAggregatesElected(adminrollup.MetricUsage, oldDay, usageRollupCaps)
	}()
	r.flushed = usageFlushed{}
	r.global = scopeUsage{}
	r.byModel = make(map[string]*scopeUsage)
	r.byProv = make(map[string]*scopeUsage)
	r.byKey = make(map[string]*scopeUsage)
	r.byUser = make(map[string]*scopeUsage)
}

func scopeKey(kind, name string) string {
	return fmt.Sprintf("%s:%s", kind, name)
}

func (r *Recorder) add(scope map[string]*scopeUsage, key string, tokens int64) {
	u := scope[key]
	if u == nil {
		u = &scopeUsage{}
		scope[key] = u
	}
	u.Requests++
	u.Tokens += tokens
}

// RecordRequest ingests token volume for one LLM request.
func (r *Recorder) RecordRequest(provider, model, keyID, userID string, inputTokens, outputTokens int) {
	if r == nil {
		return
	}
	tokens := int64(inputTokens + outputTokens)
	if tokens <= 0 {
		return
	}

	now := time.Now().UTC()
	r.mu.Lock()
	r.maybeRollDay(now)
	r.global.Requests++
	r.global.Tokens += tokens
	if model != "" {
		r.add(r.byModel, scopeKey("model", model), tokens)
	}
	if provider != "" {
		r.add(r.byProv, scopeKey("provider", provider), tokens)
	}
	if keyID != "" {
		r.add(r.byKey, scopeKey("key", keyID), tokens)
	}
	if userID != "" {
		r.add(r.byUser, scopeKey("user", userID), tokens)
	}
	entry := usageEvent{
		Time:         now.Unix(),
		Provider:     provider,
		Model:        model,
		KeyID:        keyID,
		UserID:       userID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		TotalTokens:  inputTokens + outputTokens,
	}
	r.EmitHistory(entry)

	dayKey := r.dayKey
	delta := r.usageDeltaLocked()
	r.advanceUsageFlushedLocked()
	r.mu.Unlock()

	r.QueueDelta(dayKey, delta)
}

func (r *Recorder) usageDeltaLocked() adminrollup.Delta {
	d := adminrollup.Delta{
		Totals: map[string]float64{
			"requests": float64(r.global.Requests - r.flushed.global.Requests),
			"tokens":   float64(r.global.Tokens - r.flushed.global.Tokens),
		},
		Dimensions: map[string]map[string]float64{
			"by_model":    usageScopeDelta(r.byModel, r.flushed.byModel),
			"by_provider": usageScopeDelta(r.byProv, r.flushed.byProv),
			"by_key":      usageScopeDelta(r.byKey, r.flushed.byKey),
			"by_user":     usageScopeDelta(r.byUser, r.flushed.byUser),
		},
	}
	return d
}

func usageScopeDelta(cur map[string]*scopeUsage, prev map[string]scopeUsage) map[string]float64 {
	out := make(map[string]float64)
	for key, u := range cur {
		p := prev[key]
		if dr := float64(u.Requests - p.Requests); dr != 0 {
			out[adminrollup.DimMemberField(key, "requests")] = dr
		}
		if dt := float64(u.Tokens - p.Tokens); dt != 0 {
			out[adminrollup.DimMemberField(key, "tokens")] = dt
		}
	}
	return out
}

func (r *Recorder) advanceUsageFlushedLocked() {
	if r.flushed.byModel == nil {
		r.flushed.byModel = make(map[string]scopeUsage)
	}
	if r.flushed.byProv == nil {
		r.flushed.byProv = make(map[string]scopeUsage)
	}
	if r.flushed.byKey == nil {
		r.flushed.byKey = make(map[string]scopeUsage)
	}
	if r.flushed.byUser == nil {
		r.flushed.byUser = make(map[string]scopeUsage)
	}
	r.flushed.global = r.global
	for k, v := range r.byModel {
		r.flushed.byModel[k] = *v
	}
	for k, v := range r.byProv {
		r.flushed.byProv[k] = *v
	}
	for k, v := range r.byKey {
		r.flushed.byKey[k] = *v
	}
	for k, v := range r.byUser {
		r.flushed.byUser[k] = *v
	}
}

func scopeMap(m map[string]*scopeUsage) map[string]scopeUsage {
	out := make(map[string]scopeUsage, len(m))
	for k, v := range m {
		out[k] = *v
	}
	return out
}

func (r *Recorder) rollupDataLocked() map[string]interface{} {
	return map[string]interface{}{
		"requests_today": r.global.Requests,
		"tokens_today":   r.global.Tokens,
		"by_model":       scopeMap(r.byModel),
		"by_provider":    scopeMap(r.byProv),
		"by_key":         scopeMap(r.byKey),
		"by_user":        scopeMap(r.byUser),
	}
}

type nameCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

func topScopes(m map[string]*scopeUsage, n int) []nameCount {
	type pair struct {
		name string
		v    int64
	}
	pairs := make([]pair, 0, len(m))
	for k, u := range m {
		pairs = append(pairs, pair{name: k, v: u.Tokens})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].name < pairs[j].name
	})
	if n > 0 && len(pairs) > n {
		pairs = pairs[:n]
	}
	out := make([]nameCount, len(pairs))
	for i, p := range pairs {
		out[i] = nameCount{Name: p.name, Count: p.v}
	}
	return out
}

func (r *Recorder) allCountersLocked() map[string]scopeUsage {
	out := make(map[string]scopeUsage, len(r.byModel)+len(r.byProv)+len(r.byKey)+len(r.byUser)+1)
	out["global"] = r.global
	for k, v := range r.byModel {
		out[k] = *v
	}
	for k, v := range r.byProv {
		out[k] = *v
	}
	for k, v := range r.byKey {
		out[k] = *v
	}
	for k, v := range r.byUser {
		out[k] = *v
	}
	return out
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

	var global scopeUsage
	var byModel map[string]*scopeUsage
	var byProv map[string]*scopeUsage
	var counters map[string]scopeUsage
	if localActive {
		global = r.global
		byModel = r.byModel
		byProv = r.byProv
		counters = r.allCountersLocked()
	} else {
		counters = map[string]scopeUsage{"global": {}}
	}

	snap := map[string]interface{}{
		"available":      true,
		"day":            today,
		"started_at":     startedAt.Unix(),
		"requests_today": global.Requests,
		"tokens_today":   global.Tokens,
		"top_models":     topScopes(byModel, 10),
		"top_providers":  topScopes(byProv, 10),
		"counters":       counters,
	}
	r.mu.RUnlock()

	r.MergeToday(adminrollup.MetricUsage, today, snap, usageRollupCaps)
	if localActive {
		mergeLocalUsageIntoSnap(snap, global, counters)
	}
	r.MergeHistory(adminrollup.MetricUsage, snap)
	r.MergeHourly(adminrollup.MetricUsage, snap)
	return snap
}

func mergeLocalUsageIntoSnap(snap map[string]interface{}, global scopeUsage, counters map[string]scopeUsage) {
	if snap == nil {
		return
	}
	mergeSnapInt64Max(snap, "requests_today", global.Requests)
	mergeSnapInt64Max(snap, "tokens_today", global.Tokens)

	existing, _ := snap["counters"].(map[string]scopeUsage)
	snap["counters"] = mergeScopeUsageMaps(existing, counters)

	if merged, ok := snap["counters"].(map[string]scopeUsage); ok {
		snap["top_models"] = topScopesFromCounters(merged, "model:", 10)
		snap["top_providers"] = topScopesFromCounters(merged, "provider:", 10)
	}
}

func mergeSnapInt64Max(snap map[string]interface{}, key string, local int64) {
	if local <= 0 {
		return
	}
	cur := snapInt64(snap[key])
	if local > cur {
		snap[key] = local
	}
}

func snapInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func mergeScopeUsageMaps(a, b map[string]scopeUsage) map[string]scopeUsage {
	out := make(map[string]scopeUsage, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		existing := out[k]
		if v.Requests > existing.Requests {
			existing.Requests = v.Requests
		}
		if v.Tokens > existing.Tokens {
			existing.Tokens = v.Tokens
		}
		out[k] = existing
	}
	return out
}

func topScopesFromCounters(counters map[string]scopeUsage, prefix string, n int) []nameCount {
	ptrMap := make(map[string]*scopeUsage)
	for k, v := range counters {
		if len(prefix) > 0 && len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			vv := v
			ptrMap[k] = &vv
		}
	}
	return topScopes(ptrMap, n)
}
