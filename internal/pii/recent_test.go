package pii

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
)

func TestRecorderSnapshotMergesRollupRecentEvents(t *testing.T) {
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Backend: adminrollup.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx := context.Background()
	anthropic := recentEntry{
		Time:         time.Now().Unix(),
		Provider:     "anthropic",
		KeyID:        "sk-ant-…deadbeef",
		EntityCounts: map[string]int{"PHONE_NUMBER": 1},
		EntityTotal:  1,
		BodyBytes:    200,
		DurationMs:   2,
		Outcome:      OutcomeOK,
	}
	openai := recentEntry{
		Time:         time.Now().Unix(),
		Provider:     "openai",
		KeyID:        "sk-iw-abc",
		EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
		EntityTotal:  1,
		BodyBytes:    100,
		DurationMs:   1,
		Outcome:      OutcomeOK,
	}
	if err := store.AppendRecentEvent(ctx, adminrollup.MetricPII, openai, MaxRecentEvents); err != nil {
		t.Fatalf("append openai: %v", err)
	}
	if err := store.AppendRecentEvent(ctx, adminrollup.MetricPII, anthropic, MaxRecentEvents); err != nil {
		t.Fatalf("append anthropic: %v", err)
	}

	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricPII))
	snap := r.Snapshot()

	if snap["recent_backend"] != adminrollup.BackendMemory {
		t.Fatalf("recent_backend = %v, want %q", snap["recent_backend"], adminrollup.BackendMemory)
	}
	recent := snap["recent"].([]recentEntry)
	if len(recent) != 2 {
		t.Fatalf("recent len = %d, want 2", len(recent))
	}
	if recent[0].Provider != "anthropic" || recent[1].Provider != "openai" {
		t.Fatalf("recent order = [%q, %q], want [anthropic, openai]", recent[0].Provider, recent[1].Provider)
	}
}

func TestRecorderRecordRedactionAppendsRollupRecentEvent(t *testing.T) {
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Backend: adminrollup.BackendMemory,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricPII))
	r.RecordRedaction("openai", "sk-iw-abc", map[string]int{"EMAIL_ADDRESS": 1}, 100, time.Millisecond, OutcomeOK)

	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := store.LoadRecentEventPayloads(ctx, adminrollup.MetricPII, MaxRecentEvents)
		if err != nil {
			t.Fatalf("LoadRecentEventPayloads: %v", err)
		}
		if len(raw) == 1 {
			snap := r.Snapshot()
			recent := snap["recent"].([]recentEntry)
			if len(recent) != 1 {
				t.Fatalf("snapshot recent len = %d, want 1", len(recent))
			}
			if recent[0].Provider != "openai" {
				t.Fatalf("recent[0].Provider = %q, want openai", recent[0].Provider)
			}
			if snap["recent_backend"] != adminrollup.BackendMemory {
				t.Fatalf("recent_backend = %v, want %q", snap["recent_backend"], adminrollup.BackendMemory)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rollup recent len = %d, want 1 (timed out waiting for async write)", len(raw))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRecorderRecentBackendMemoryWhenRollupsUnbound(t *testing.T) {
	r := NewRecorder()
	r.RecordRedaction("openai", "iw:abc", nil, 0, 0, OutcomeOK)
	snap := r.Snapshot()
	if snap["recent_backend"] != "memory" {
		t.Fatalf("recent_backend = %v, want memory", snap["recent_backend"])
	}
}
