package usagestats

import (
	"testing"
	"time"
)

func TestRecorderAggregatesByScope(t *testing.T) {
	r := NewRecorder()

	r.RecordRequest("openai", "gpt-4o-mini", "iw:abc", "user-1", 100, 50)
	r.RecordRequest("openai", "gpt-4o-mini", "iw:abc", "user-1", 80, 40)
	r.RecordRequest("anthropic", "claude-haiku", "iw:zzz", "user-2", 200, 100)

	snap := r.Snapshot()

	if got := snap["available"].(bool); !got {
		t.Fatal("snapshot should be available")
	}
	if got := snap["requests_today"].(int64); got != 3 {
		t.Fatalf("requests_today = %d, want 3", got)
	}
	// (100+50)+(80+40)+(200+100) = 570
	if got := snap["tokens_today"].(int64); got != 570 {
		t.Fatalf("tokens_today = %d, want 570", got)
	}

	topModels := snap["top_models"].([]nameCount)
	if len(topModels) != 2 {
		t.Fatalf("top_models len = %d, want 2", len(topModels))
	}
	// claude-haiku has 300 tokens > gpt-4o-mini 270, so it ranks first.
	if topModels[0].Name != "model:claude-haiku" || topModels[0].Count != 300 {
		t.Fatalf("top model = %+v, want model:claude-haiku/300", topModels[0])
	}

	counters := snap["counters"].(map[string]scopeUsage)
	if g := counters["global"]; g.Requests != 3 || g.Tokens != 570 {
		t.Fatalf("global counter = %+v", g)
	}
	if k := counters["key:iw:abc"]; k.Requests != 2 || k.Tokens != 270 {
		t.Fatalf("key counter = %+v, want 2/270", k)
	}
	if u := counters["provider:anthropic"]; u.Requests != 1 || u.Tokens != 300 {
		t.Fatalf("provider counter = %+v, want 1/300", u)
	}
}

func TestRecorderIgnoresNonPositiveTokens(t *testing.T) {
	r := NewRecorder()
	r.RecordRequest("openai", "gpt-4o", "iw:abc", "user-1", 0, 0)
	r.RecordRequest("openai", "gpt-4o", "iw:abc", "user-1", -5, 2)

	snap := r.Snapshot()
	if got := snap["requests_today"].(int64); got != 0 {
		t.Fatalf("requests_today = %d, want 0 (non-positive tokens ignored)", got)
	}
}

func TestRecorderOmitsEmptyScopes(t *testing.T) {
	r := NewRecorder()
	// No model/provider/key/user — only the global counter should move.
	r.RecordRequest("", "", "", "", 10, 5)

	snap := r.Snapshot()
	if got := snap["requests_today"].(int64); got != 1 {
		t.Fatalf("requests_today = %d, want 1", got)
	}
	counters := snap["counters"].(map[string]scopeUsage)
	if len(counters) != 1 {
		t.Fatalf("counters = %+v, want only global", counters)
	}
	if _, ok := counters["global"]; !ok {
		t.Fatalf("missing global counter: %+v", counters)
	}
}

func TestRecorderRollsDayBucket(t *testing.T) {
	r := NewRecorder()
	// Pretend the recorder is holding yesterday's data.
	r.dayKey = time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	r.global = scopeUsage{Requests: 5, Tokens: 5000}
	r.byModel["model:old"] = &scopeUsage{Requests: 5, Tokens: 5000}

	r.RecordRequest("openai", "gpt-4o-mini", "iw:abc", "user-1", 10, 5)

	snap := r.Snapshot()
	if got := snap["requests_today"].(int64); got != 1 {
		t.Fatalf("requests_today after roll = %d, want 1", got)
	}
	if got := snap["tokens_today"].(int64); got != 15 {
		t.Fatalf("tokens_today after roll = %d, want 15", got)
	}
	// Yesterday's model bucket must not survive the rollover.
	counters := snap["counters"].(map[string]scopeUsage)
	if _, ok := counters["model:old"]; ok {
		t.Fatalf("stale bucket carried over rollover: %+v", counters)
	}
}

func TestTopScopesOrderingAndLimit(t *testing.T) {
	m := map[string]*scopeUsage{
		"model:a": {Requests: 1, Tokens: 10},
		"model:b": {Requests: 1, Tokens: 30},
		"model:c": {Requests: 1, Tokens: 20},
	}
	top := topScopes(m, 2)
	if len(top) != 2 {
		t.Fatalf("limit not applied: len = %d", len(top))
	}
	if top[0].Name != "model:b" || top[1].Name != "model:c" {
		t.Fatalf("ordering wrong: %+v", top)
	}
}

func TestTopScopesTieBreaksByName(t *testing.T) {
	m := map[string]*scopeUsage{
		"model:b": {Tokens: 10},
		"model:a": {Tokens: 10},
	}
	top := topScopes(m, 0)
	if top[0].Name != "model:a" || top[1].Name != "model:b" {
		t.Fatalf("equal tokens should tie-break by name asc: %+v", top)
	}
}

func TestRecorderNilSafe(t *testing.T) {
	var r *Recorder
	// Must not panic.
	r.RecordRequest("openai", "gpt-4o", "iw:abc", "user-1", 1, 1)
	if got := r.Snapshot()["available"].(bool); got {
		t.Fatal("nil recorder should report unavailable")
	}
}
