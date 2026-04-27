package circuit

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestTransport_LogMode_PassThroughOnSuccess verifies that ModeLog performs
// exactly one upstream call on a 200 and returns the real response
// unmodified — no synthetic wrapping, no extra headers.
func TestTransport_LogMode_PassThroughOnSuccess(t *testing.T) {
	calls := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return makeResp(200), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeLog,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 2, // would retry in enforce mode — must NOT here
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 pass-through, got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("ModeLog must issue exactly 1 upstream call, got %d", calls)
	}
}

// TestTransport_LogMode_RecordsFailuresButPassesThrough verifies that ModeLog
// records terminal failures in the Store (so cross-instance counters stay
// accurate during a shadow rollout) yet returns the raw upstream 503 — never
// a synthetic DefaultDegradedSignal body.
func TestTransport_LogMode_RecordsFailuresButPassesThrough(t *testing.T) {
	calls := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeLog,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 2, // would retry in enforce — must NOT here
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ModeLog must return the real upstream 503, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("ModeLog must NOT inject DefaultDegradedSignal; body=%q", b)
	}
	if calls != 1 {
		t.Fatalf("ModeLog must issue exactly 1 upstream call even on failure, got %d", calls)
	}

	// Counterfactual state advanced from Closed → Open in the Store so
	// operators can measure would-trip rate during shadow rollout.
	if state, _ := store.GetState(context.Background(), "openai"); state != StateOpen {
		t.Fatalf("ModeLog should have recorded the failure and flipped would-be-state to Open, got %s", state)
	}
}

// TestTransport_LogMode_DoesNotFastFailOnOpenCircuit verifies that even when
// the circuit is already Open, ModeLog still performs the real upstream
// round trip and returns its response (the whole point of shadow mode).
func TestTransport_LogMode_DoesNotFastFailOnOpenCircuit(t *testing.T) {
	calls := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls++
		return makeResp(200), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeLog,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	store.RecordTerminalFailure(context.Background(), "openai") //nolint:errcheck

	tr := NewTransport(inner, store, cfg, "openai", nil)
	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("ModeLog must let traffic through even when circuit is open; got %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call, got %d", calls)
	}
}

// TestConfig_Defaults_ModeIsLog verifies that an unset Mode defaults to
// ModeLog (observe-only) — the safe default for rollouts.
func TestConfig_Defaults_ModeIsLog(t *testing.T) {
	if got := (Config{}).Defaults().Mode; got != ModeLog {
		t.Fatalf("empty Mode should default to ModeLog, got %q", got)
	}
	if got := (Config{Mode: "bogus"}).Defaults().Mode; got != ModeLog {
		t.Fatalf("unknown Mode should default to ModeLog, got %q", got)
	}
	if got := (Config{Mode: ModeEnforce}).Defaults().Mode; got != ModeEnforce {
		t.Fatalf("explicit ModeEnforce must be preserved, got %q", got)
	}
}
