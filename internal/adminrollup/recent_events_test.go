package adminrollup

import (
	"context"
	"testing"
)

func TestStoreRecentEventsAppendAndLoad(t *testing.T) {
	store, err := NewStore(Config{Enabled: true, Backend: BackendMemory})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ctx := context.Background()
	if err := store.AppendRecentEvent(ctx, MetricPII, map[string]any{"provider": "openai", "time": 1}, 50); err != nil {
		t.Fatalf("AppendRecentEvent 1: %v", err)
	}
	if err := store.AppendRecentEvent(ctx, MetricPII, map[string]any{"provider": "anthropic", "time": 2}, 50); err != nil {
		t.Fatalf("AppendRecentEvent 2: %v", err)
	}

	raw, err := store.LoadRecentEventPayloads(ctx, MetricPII, 50)
	if err != nil {
		t.Fatalf("LoadRecentEventPayloads: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("len(raw) = %d, want 2", len(raw))
	}
	if string(raw[0]) != `{"provider":"anthropic","time":2}` {
		t.Fatalf("raw[0] = %s", raw[0])
	}
}

func TestStoreRecentEventsTrim(t *testing.T) {
	store, err := NewStore(Config{Enabled: true, Backend: BackendMemory})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := store.AppendRecentEvent(ctx, MetricPII, map[string]int{"n": i}, 3); err != nil {
			t.Fatalf("AppendRecentEvent: %v", err)
		}
	}
	raw, err := store.LoadRecentEventPayloads(ctx, MetricPII, 3)
	if err != nil {
		t.Fatalf("LoadRecentEventPayloads: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("len(raw) = %d, want 3", len(raw))
	}
}
