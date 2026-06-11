package pii

import (
	"testing"
	"time"
)

func TestRecorderAggregates(t *testing.T) {
	r := NewRecorder()

	r.RecordRedaction("openai", "iw:abcdefgh999", map[string]int{"EMAIL_ADDRESS": 2, "PHONE_NUMBER": 1}, 1000, 5*time.Millisecond, OutcomeOK)
	r.RecordRedaction("openai", "iw:abcdefgh999", nil, 500, 2*time.Millisecond, OutcomeOK)
	r.RecordRedaction("anthropic", "iw:zzzzzzzz0001", map[string]int{"EMAIL_ADDRESS": 1}, 800, 3*time.Millisecond, OutcomeOK)
	r.RecordRedaction("gemini", "iw:zzzzzzzz0001", nil, 0, 0, OutcomeFailOpen)
	r.RecordRedaction("openai", "iw:abcdefgh999", nil, 2_000_000, 0, OutcomeOversize)

	snap := r.Snapshot()

	if got := snap["requests_scanned"].(int64); got != 5 {
		t.Fatalf("requests_scanned = %d, want 5", got)
	}
	if got := snap["requests_with_pii"].(int64); got != 2 {
		t.Fatalf("requests_with_pii = %d, want 2", got)
	}
	if got := snap["entities_total"].(int64); got != 4 {
		t.Fatalf("entities_total = %d, want 4", got)
	}
	if got := snap["fail_open"].(int64); got != 1 {
		t.Fatalf("fail_open = %d, want 1", got)
	}
	if got := snap["oversize"].(int64); got != 1 {
		t.Fatalf("oversize = %d, want 1", got)
	}

	// detection_rate = requests_with_pii / clean scanned (exclude fail/oversize)
	// clean = 5 - 1 fail_open - 1 oversize = 3; rate = 2/3
	if got := snap["detection_rate"].(float64); got < 0.66 || got > 0.67 {
		t.Fatalf("detection_rate = %f, want ~0.667", got)
	}

	byEntity := snap["by_entity"].([]kv)
	if len(byEntity) != 2 || byEntity[0].Name != "EMAIL_ADDRESS" || byEntity[0].Count != 3 {
		t.Fatalf("by_entity top = %+v, want EMAIL_ADDRESS:3 first", byEntity)
	}

	recent := snap["recent"].([]recentEntry)
	if len(recent) != 5 {
		t.Fatalf("recent len = %d, want 5", len(recent))
	}
	// newest-first: last recorded was the oversize event
	if recent[0].Outcome != OutcomeOversize {
		t.Fatalf("recent[0].Outcome = %q, want oversize", recent[0].Outcome)
	}
}

func TestRecorderRingBufferBounded(t *testing.T) {
	r := NewRecorder()
	for i := 0; i < MaxRecentEvents+25; i++ {
		r.RecordRedaction("openai", "k", map[string]int{"EMAIL_ADDRESS": 1}, 10, time.Millisecond, OutcomeOK)
	}
	recent := r.Snapshot()["recent"].([]recentEntry)
	if len(recent) != MaxRecentEvents {
		t.Fatalf("recent len = %d, want %d", len(recent), MaxRecentEvents)
	}
}

func TestNilRecorderSafe(t *testing.T) {
	var r *Recorder
	r.RecordRedaction("openai", "k", nil, 0, 0, OutcomeOK)
	if snap := r.Snapshot(); snap["available"].(bool) {
		t.Fatal("nil recorder should report available=false")
	}
}
