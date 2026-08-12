package circuit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newHangAbortTestTransport builds a Transport whose Config carries
// hangDisconnectFailureSeconds, wired to a fresh MemoryStore so tests can
// assert directly on recorded failure counts via GetStats.
func newHangAbortTestTransport(hangDisconnectFailureSeconds int) *Transport {
	cfg := Config{
		Enabled:                      true,
		Mode:                         ModeEnforce,
		FailureThreshold:             3,
		WindowSeconds:                60,
		CooldownSeconds:              300,
		MaxTransientRetries:          1,
		MaxRateLimitRetries:          1,
		HangDisconnectFailureSeconds: hangDisconnectFailureSeconds,
	}.Defaults()
	store := NewMemoryStore(cfg)
	return NewTransport(nil, store, cfg, "openai", nil)
}

func TestRecordHangAbort_RecordsFailureAboveThreshold(t *testing.T) {
	tr := newHangAbortTestTransport(60)
	req := dummyRequest()

	tr.recordHangAbort(req, "openai", context.Canceled, 61*time.Second)

	stats, err := tr.store.GetStats(context.Background(), "openai")
	require.NoError(t, err)
	require.Equal(t, 1, stats.Failures, "a disconnect that outlasted the threshold must be credited")
}

func TestRecordHangAbort_RecordsFailureAtExactThreshold(t *testing.T) {
	tr := newHangAbortTestTransport(60)
	req := dummyRequest()

	tr.recordHangAbort(req, "openai", context.Canceled, 60*time.Second)

	stats, err := tr.store.GetStats(context.Background(), "openai")
	require.NoError(t, err)
	require.Equal(t, 1, stats.Failures, "a disconnect exactly at the threshold must be credited")
}

func TestRecordHangAbort_NotRecordedBelowThreshold(t *testing.T) {
	tr := newHangAbortTestTransport(60)
	req := dummyRequest()

	tr.recordHangAbort(req, "openai", context.Canceled, 59*time.Second)

	stats, err := tr.store.GetStats(context.Background(), "openai")
	require.NoError(t, err)
	require.Equal(t, 0, stats.Failures, "a fast cancel below the threshold must not be credited")
}

func TestRecordHangAbort_DisabledByDefault(t *testing.T) {
	// HangDisconnectFailureSeconds left at zero: the feature must be a no-op
	// even for a hang that waited far longer than any reasonable threshold.
	tr := newHangAbortTestTransport(0)
	req := dummyRequest()

	tr.recordHangAbort(req, "openai", context.Canceled, 10*time.Minute)

	stats, err := tr.store.GetStats(context.Background(), "openai")
	require.NoError(t, err)
	require.Equal(t, 0, stats.Failures, "disabled (zero) must never record, regardless of wait duration")
}

// TestTransport_RunWithRetries_HangDisconnect_EndToEnd exercises the real
// call site in runWithRetries: an inner transport that blocks until the
// request context is canceled, returning (nil, ctx.Err()) — the exact shape
// of a caller disconnecting mid-hang. Uses real timing (~1s) rather than the
// direct-call unit tests above so the RoundTrip wiring itself is covered,
// not just recordHangAbort in isolation.
func TestTransport_RunWithRetries_HangDisconnect_EndToEnd(t *testing.T) {
	blocked := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	cfg := Config{
		Enabled:                      true,
		Mode:                         ModeEnforce,
		FailureThreshold:             3,
		WindowSeconds:                60,
		CooldownSeconds:              300,
		MaxTransientRetries:          1,
		MaxRateLimitRetries:          1,
		HangDisconnectFailureSeconds: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(blocked, store, cfg, "openai", nil)

	// context.WithCancel + an explicit cancel() (not WithTimeout) mirrors what
	// net/http actually does when a client disconnects mid-request: the
	// server hands handlers a context that is canceled with context.Canceled,
	// never context.DeadlineExceeded. That distinction matters here because
	// classifyNetworkError treats the two very differently.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")

	go func() {
		time.Sleep(1100 * time.Millisecond)
		cancel()
	}()

	_, err := tr.RoundTrip(req)
	require.Error(t, err)
	require.True(t, errors.Is(err, context.Canceled))

	stats, statsErr := store.GetStats(context.Background(), "openai")
	require.NoError(t, statsErr)
	require.Equal(t, 1, stats.Failures, "a disconnect past hang_disconnect_failure_seconds must reach the breaker even though the caller is gone")
}

func TestTransport_RunWithRetries_ResponseReceived_NotCreditedAsHangAbort(t *testing.T) {
	// A real upstream response arrived — this must never be mistaken for a
	// hang-disconnect even with the feature enabled and a zero threshold.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(200), nil
	})

	cfg := Config{
		Enabled:                      true,
		Mode:                         ModeEnforce,
		FailureThreshold:             3,
		WindowSeconds:                60,
		CooldownSeconds:              300,
		MaxTransientRetries:          1,
		MaxRateLimitRetries:          1,
		HangDisconnectFailureSeconds: 0, // any wait at all would exceed a zero threshold if misapplied
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	stats, statsErr := store.GetStats(context.Background(), "openai")
	require.NoError(t, statsErr)
	require.Equal(t, 0, stats.Failures)
}

func TestTransport_RunWithRetries_FastCancel_NotCreditedWhenBelowThreshold(t *testing.T) {
	blocked := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	cfg := Config{
		Enabled:                      true,
		Mode:                         ModeEnforce,
		FailureThreshold:             3,
		WindowSeconds:                60,
		CooldownSeconds:              300,
		MaxTransientRetries:          1,
		MaxRateLimitRetries:          1,
		HangDisconnectFailureSeconds: 60,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(blocked, store, cfg, "openai", nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := tr.RoundTrip(req)
	require.Error(t, err)

	stats, statsErr := store.GetStats(context.Background(), "openai")
	require.NoError(t, statsErr)
	require.Equal(t, 0, stats.Failures, "a cancel well below hang_disconnect_failure_seconds must not be credited")
}
