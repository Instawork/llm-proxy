package middleware

import (
	"sync"
	"testing"
	"time"
)

type fakeDogstatsd struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeDogstatsd) Incr(name string, tags []string, rate float64) error {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
	return nil
}

func (f *fakeDogstatsd) Distribution(name string, _ float64, _ []string, _ float64) error {
	return f.Incr(name, nil, 1.0)
}

func TestEmitPIIRedactionMetrics(t *testing.T) {
	metrics := &fakeDogstatsd{}
	emitPIIRedactionMetrics(metrics, "openai", "ok", map[string]int{"US_SSN": 2, "PERSON": 1}, 50*time.Millisecond)

	if len(metrics.calls) < 4 {
		t.Fatalf("expected at least 4 metric calls, got %d (%v)", len(metrics.calls), metrics.calls)
	}
	if metrics.calls[0] != "pii.redaction" {
		t.Fatalf("first metric want pii.redaction, got %q", metrics.calls[0])
	}
}
