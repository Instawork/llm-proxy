package adminrollup

import (
	"context"
	"testing"
)

func snap(provs map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"providers": provs}
}

func TestCircuitDayPeakTakesElementwiseMax(t *testing.T) {
	p := newCircuitDayPeak()

	// Window gauge bounces 3 -> 7 -> 2 for openai; peak must stick at 7.
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 3, "state": "closed"},
	}))
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 7, "state": "open", "rollup_open": true},
	}))
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 2, "state": "closed"},
	}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 7 {
		t.Fatalf("total_failures = %d, want 7 (peak, not sum)", got)
	}
	prov := out["providers"].(map[string]interface{})["openai"].(map[string]interface{})
	if got := prov["failures"].(int); got != 7 {
		t.Fatalf("openai peak failures = %d, want 7", got)
	}
	// rollup_open is sticky once seen true within the day.
	if got := prov["rollup_open"].(bool); !got {
		t.Fatal("rollup_open should remain true after being observed open")
	}
	// state tracks the last observed value.
	if got := prov["state"].(string); got != "closed" {
		t.Fatalf("state = %q, want last-observed 'closed'", got)
	}
}

func TestCircuitDayPeakMultipleProviders(t *testing.T) {
	p := newCircuitDayPeak()
	p.merge(snap(map[string]interface{}{
		"openai":    map[string]interface{}{"failures": 4},
		"anthropic": map[string]interface{}{"failures": 1},
	}))
	p.merge(snap(map[string]interface{}{
		"anthropic": map[string]interface{}{"failures": 6},
	}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 10 {
		t.Fatalf("total_failures = %d, want 10 (4 + 6 peaks)", got)
	}
}

func TestCircuitDayPeakHandlesFloatFromRedis(t *testing.T) {
	p := newCircuitDayPeak()
	// Values round-tripped through JSON/Redis arrive as float64.
	p.merge(snap(map[string]interface{}{
		"gemini": map[string]interface{}{"failures": float64(5)},
	}))
	prov := p.payload()["providers"].(map[string]interface{})["gemini"].(map[string]interface{})
	if got := prov["failures"].(int); got != 5 {
		t.Fatalf("failures from float64 = %d, want 5", got)
	}
}

func TestCircuitDayPeakIgnoresMalformedSnapshots(t *testing.T) {
	p := newCircuitDayPeak()
	p.merge(map[string]interface{}{"providers": "not-a-map"})
	p.merge(map[string]interface{}{}) // no providers key
	p.merge(snap(map[string]interface{}{"openai": "not-a-map"}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 0 {
		t.Fatalf("total_failures = %d, want 0 for malformed input", got)
	}
}

func TestSnapIntCoercions(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int
	}{
		{int(3), 3},
		{int64(4), 4},
		{float64(5.9), 5},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := snapInt(c.in); got != c.want {
			t.Fatalf("snapInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSnapshotCircuitNilStore(t *testing.T) {
	out := SnapshotCircuit(context.Background(), nil, []string{"openai"}, 3, 300)
	if got := out["total_failures"].(int); got != 0 {
		t.Fatalf("nil store total_failures = %d, want 0", got)
	}
	if provs := out["providers"].(map[string]interface{}); len(provs) != 0 {
		t.Fatalf("nil store providers = %+v, want empty", provs)
	}
}
