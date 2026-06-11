package coststats

import (
	"testing"
	"time"
)

func TestRecorderAggregatesByKeyAndProvider(t *testing.T) {
	r := NewRecorder()

	r.RecordRequest("openai", "iw:abcdefgh999", "", "gpt-4o-mini", 0.0012, 0.0008, 0.0004, 100, 50)
	r.RecordRequest("openai", "iw:abcdefgh999", "", "gpt-4o-mini", 0.0008, 0.0005, 0.0003, 80, 40)
	r.RecordRequest("anthropic", "iw:zzzzzzzz0001", "", "claude-haiku", 0.002, 0.0015, 0.0005, 200, 100)

	snap := r.Snapshot()

	if got := snap["spend_today_usd"].(float64); got < 0.0039 || got > 0.0041 {
		t.Fatalf("spend_today_usd = %v, want ~0.004", got)
	}
	if got := snap["requests_today"].(int64); got != 3 {
		t.Fatalf("requests_today = %d, want 3", got)
	}

	byKey := snap["by_key"].([]keySpend)
	if len(byKey) != 2 {
		t.Fatalf("by_key len = %d, want 2", len(byKey))
	}
	if byKey[0].KeyID != "iw:abcdefgh999" || byKey[0].Requests != 2 {
		t.Fatalf("top by_key = %+v", byKey[0])
	}

	byProvider := snap["by_provider"].([]providerSpend)
	if len(byProvider) != 2 {
		t.Fatalf("by_provider len = %d, want 2", len(byProvider))
	}
	if byProvider[0].Name != "openai" && byProvider[1].Name != "openai" {
		t.Fatalf("by_provider missing openai = %+v", byProvider)
	}

	recent := snap["recent"].([]recentEntry)
	if len(recent) != 3 || recent[0].Provider != "anthropic" {
		t.Fatalf("recent newest = %+v", recent[0])
	}
}

func TestRecorderRollsDayBucket(t *testing.T) {
	r := NewRecorder()
	r.dayKey = time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	r.spendTodayUSD = 9.99
	r.requestsToday = 5

	r.RecordRequest("openai", "iw:abc", "", "gpt-4o-mini", 0.01, 0.006, 0.004, 10, 5)

	snap := r.Snapshot()
	if got := snap["spend_today_usd"].(float64); got != 0.01 {
		t.Fatalf("spend after roll = %v, want 0.01", got)
	}
	if got := snap["requests_today"].(int64); got != 1 {
		t.Fatalf("requests after roll = %d, want 1", got)
	}
}

func TestRecorderNilSafe(t *testing.T) {
	var r *Recorder
	r.RecordRequest("openai", "", "", "", 1, 0.6, 0.4, 1, 1)
	if got := r.Snapshot()["available"].(bool); got {
		t.Fatal("nil recorder should report unavailable")
	}
}
