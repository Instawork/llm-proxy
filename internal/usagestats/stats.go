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
	r.ArchiveDay(r.dayKey, r.rollupDataLocked())
	r.dayKey = day
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
	rollup := r.rollupDataLocked()
	r.mu.Unlock()

	r.QueueToday(dayKey, rollup)
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
	r.mu.RLock()
	snap := map[string]interface{}{
		"available":      true,
		"day":            r.dayKey,
		"started_at":     r.startedAt.Unix(),
		"requests_today": r.global.Requests,
		"tokens_today":   r.global.Tokens,
		"top_models":     topScopes(r.byModel, 10),
		"top_providers":  topScopes(r.byProv, 10),
		"counters":       r.allCountersLocked(),
	}
	r.mu.RUnlock()

	r.MergeHistory(adminrollup.MetricUsage, snap)
	return snap
}
