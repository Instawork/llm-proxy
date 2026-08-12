package circuit

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newTimeoutBudgetTestTransport builds a Transport with the timeout-budget
// feature enabled (ceiling == maxCeiling) and a small MaxTransientRetries so
// tests stay fast.
func newTimeoutBudgetTestTransport(inner http.RoundTripper, maxCeiling time.Duration) *Transport {
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    5,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 2,
		MaxRateLimitRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	return NewTransport(inner, store, cfg, "openai", nil, WithMaxTimeoutBudget(maxCeiling))
}

func budgetRequest(timeoutMs string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if timeoutMs != "" {
		req.Header.Set(TimeoutBudgetHeader, timeoutMs)
	}
	return req
}

func TestTransport_TimeoutBudget_TaggedDegradedWithinBudget(t *testing.T) {
	// Inner transport hangs forever (until its context is canceled) —
	// simulates a provider that never responds.
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	tr := newTimeoutBudgetTestTransport(inner, 300*time.Second)

	start := time.Now()
	resp, err := tr.RoundTrip(budgetRequest("1500"))
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), DefaultDegradedSignal)
	require.Less(t, elapsed, 10*time.Second, "must return well inside the 300s provider ceiling, bounded by the 1.5s budget")
}

func TestTransport_TimeoutBudget_ClampedToCeiling_NeverExtendsWait(t *testing.T) {
	// A caller-requested budget larger than the provider's ceiling must be
	// clamped down to the ceiling, not honoured verbatim.
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	tr := newTimeoutBudgetTestTransport(inner, 1*time.Second)

	start := time.Now()
	resp, err := tr.RoundTrip(budgetRequest("300000")) // 300s requested, ceiling is 1s
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Less(t, elapsed, 5*time.Second, "clamp to the 1s ceiling, not the requested 300s")
}

func TestTransport_TimeoutBudget_StreamingSurvivesPastBudget(t *testing.T) {
	// Headers arrive well inside the budget; the body then takes longer to
	// finish streaming than the budget allowed. Uses a real httptest.Server
	// behind a real *http.Transport (not the roundTripFunc fakes used
	// elsewhere in this file) because a fake body that ignores the request
	// context can't detect roundTripWithBudget cancelling the budget
	// context too early — only a real net/http connection reproduces that
	// failure mode, where the request context governs body reads too.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk-1"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
		_, _ = w.Write([]byte("chunk-2"))
	}))
	defer srv.Close()

	tr := newTimeoutBudgetTestTransport(http.DefaultTransport, 10*time.Second)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	req.Header.Set(TimeoutBudgetHeader, "200") // clamped up to minTimeoutBudget (1s)

	go func() {
		time.Sleep(1300 * time.Millisecond) // longer than the effective 1s budget
		close(release)
	}()

	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, "chunk-1chunk-2", string(body))
}

func TestTransport_TimeoutBudget_SkipsRetryWhenBudgetExhausted(t *testing.T) {
	var attempts int32
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	// MaxTransientRetries=2 would normally mean up to 3 attempts; the
	// budget (1s, the floor) should only leave room for one before the
	// deadline-exhausted check at the top of the loop returns terminal.
	tr := newTimeoutBudgetTestTransport(inner, 1*time.Second)

	resp, err := tr.RoundTrip(budgetRequest("1"))
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.LessOrEqual(t, int(attempts), 1, "budget exhausted after the first attempt must skip further retries")
}

func TestTimeoutBudgetFromRequest_InvalidValuesIgnored(t *testing.T) {
	ceiling := 60 * time.Second
	cases := []string{"not-a-number", "-500", "0", ""}
	for _, raw := range cases {
		req := budgetRequest(raw)
		_, ok := timeoutBudgetFromRequest(req, ceiling)
		require.False(t, ok, "value %q must be ignored (falls back to no budget)", raw)
	}
}

func TestTimeoutBudgetFromRequest_DisabledWhenNoCeilingConfigured(t *testing.T) {
	// Even a well-formed value must be ignored when the provider's
	// transport was never wired with WithMaxTimeoutBudget.
	req := budgetRequest("5000")
	_, ok := timeoutBudgetFromRequest(req, 0)
	require.False(t, ok)
}

func TestTimeoutBudgetFromRequest_ClampsBelowMinimum(t *testing.T) {
	req := budgetRequest("10") // 10ms, below minTimeoutBudget
	budget, ok := timeoutBudgetFromRequest(req, 60*time.Second)
	require.True(t, ok)
	require.Equal(t, minTimeoutBudget, budget)
}

func TestTimeoutBudgetFromRequest_HugeValueClampsToCeilingWithoutOverflow(t *testing.T) {
	// A caller-supplied value near math.MaxInt64 would overflow int64 if
	// converted to time.Duration before being clamped, silently wrapping to
	// an arbitrary (possibly tiny or negative) duration instead of the
	// intended ceiling.
	req := budgetRequest(strconv.FormatInt(math.MaxInt64, 10))
	budget, ok := timeoutBudgetFromRequest(req, 60*time.Second)
	require.True(t, ok)
	require.Equal(t, 60*time.Second, budget)
}

func TestTimeoutBudgetFromRequest_QueryParamFallback(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/openai/v1/chat/completions?"+TimeoutBudgetQueryParam+"="+strconv.Itoa(5000), nil)
	budget, ok := timeoutBudgetFromRequest(req, 60*time.Second)
	require.True(t, ok)
	require.Equal(t, 5*time.Second, budget)
}

func TestTransport_TimeoutBudget_HeaderStrippedBeforeUpstream(t *testing.T) {
	var sawHeader, sawQueryParam string
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawHeader = r.Header.Get(TimeoutBudgetHeader)
		sawQueryParam = r.URL.Query().Get(TimeoutBudgetQueryParam)
		return makeResp(200), nil
	})
	tr := newTimeoutBudgetTestTransport(inner, 60*time.Second)

	req := budgetRequest("5000")
	q := req.URL.Query()
	q.Set(TimeoutBudgetQueryParam, "5000")
	req.URL.RawQuery = q.Encode()

	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	require.Empty(t, sawHeader, "header must be stripped before reaching the inner transport")
	require.Empty(t, sawQueryParam, "query param must be stripped before reaching the inner transport")
}

func TestHasTimeoutBudgetMarkers_DetectsPresentButEmptyHeader(t *testing.T) {
	// http.Header.Get returns "" for both an absent header and a present
	// header with an empty value, so a bare "X-LLM-Proxy-Timeout-Ms:" must
	// still be detected (and therefore stripped) rather than silently
	// treated as if the header were never sent.
	req := dummyRequest()
	req.Header.Set(TimeoutBudgetHeader, "")
	require.True(t, hasTimeoutBudgetMarkers(req))
}

func TestTransport_TimeoutBudget_NoHeaderBehavesUnchanged(t *testing.T) {
	// No header/param at all: behaviour must be identical to the feature
	// not existing, even with a ceiling configured.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(200), nil
	})
	tr := newTimeoutBudgetTestTransport(inner, 60*time.Second)

	resp, err := tr.RoundTrip(dummyRequest())
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
}
