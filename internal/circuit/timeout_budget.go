package circuit

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// TimeoutBudgetHeader lets a caller shorten (never extend) how long this
// proxy will spend trying to produce response headers for a request,
// counting across any transient/rate-limit retries runWithRetries performs
// internally. Value is an integer count of milliseconds.
//
// Motivation: a caller whose own deadline is shorter than this provider's
// response_header_timeout_seconds + retry budget always disconnects before
// a pure provider hang could surface as a synthesised degraded 503 — the
// caller sees a bare timeout with none of the [LLM_PROXY_PROVIDER_DEGRADED]
// signal it could otherwise fail over on. A caller that knows its own call
// should finish in, say, 5 seconds (a routing decision, not a full
// generation) can set this header to get the tagged degraded response
// BEFORE its own deadline fires, instead of racing it.
//
// Only takes effect when the provider's transport was constructed with
// WithMaxTimeoutBudget(ceiling > 0) — operators opt in per provider. The
// requested value is clamped to [minTimeoutBudget, ceiling]: callers can
// only shorten the proxy's wait, never extend it past what the provider's
// own response_header_timeout_seconds allows.
//
// Only governs the wait for response HEADERS. Once they arrive, the budget
// has no further effect — a streaming response keeps flowing past the
// budget's deadline, exactly like a normal request with no budget set.
const TimeoutBudgetHeader = "X-LLM-Proxy-Timeout-Ms"

// TimeoutBudgetQueryParam is the URL query parameter equivalent of
// TimeoutBudgetHeader, for SDKs that cannot set custom headers. Mirrors
// BypassQueryParam's rationale.
const TimeoutBudgetQueryParam = "llm_proxy_timeout_ms"

// minTimeoutBudget floors a caller-requested budget so a near-zero or
// negative value can't turn every request into an instant synthetic
// failure — that would make the header a trivial way to force fast-fails
// without ever giving upstream a real chance to answer.
const minTimeoutBudget = 1 * time.Second

// timeoutBudgetFromRequest returns the caller-requested header-wait budget
// for req, clamped to [minTimeoutBudget, ceiling]. ok is false when the
// feature is unavailable (ceiling <= 0, i.e. no WithMaxTimeoutBudget was
// configured for this provider) or the caller didn't supply a usable
// positive integer value.
func timeoutBudgetFromRequest(req *http.Request, ceiling time.Duration) (budget time.Duration, ok bool) {
	if ceiling <= 0 || req == nil {
		return 0, false
	}
	raw := req.Header.Get(TimeoutBudgetHeader)
	if raw == "" && req.URL != nil {
		raw = req.URL.Query().Get(TimeoutBudgetQueryParam)
	}
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return 0, false
	}
	budget = time.Duration(ms) * time.Millisecond
	if budget < minTimeoutBudget {
		budget = minTimeoutBudget
	}
	if budget > ceiling {
		budget = ceiling
	}
	return budget, true
}

// hasTimeoutBudgetMarkers returns true if req carries the timeout-budget
// header or query param, regardless of whether the value is usable —
// mirrors hasBypassMarkers so an unparsable value still gets stripped
// before reaching upstream.
func hasTimeoutBudgetMarkers(req *http.Request) bool {
	if req == nil {
		return false
	}
	if req.Header.Get(TimeoutBudgetHeader) != "" {
		return true
	}
	if req.URL != nil {
		return req.URL.Query().Has(TimeoutBudgetQueryParam)
	}
	return false
}

// stripTimeoutBudgetMarkers removes the timeout-budget header/query param
// before forwarding upstream, mirroring stripBypassMarkers: this signal is
// purely proxy-internal and must never leak into provider request logs.
// Callers pass the result of req.Clone(ctx) so the caller's request object
// is never mutated.
func stripTimeoutBudgetMarkers(req *http.Request) *http.Request {
	if req == nil {
		return req
	}
	out := req.Clone(req.Context())
	out.Header.Del(TimeoutBudgetHeader)
	if out.URL != nil {
		q := out.URL.Query()
		if q.Has(TimeoutBudgetQueryParam) {
			q.Del(TimeoutBudgetQueryParam)
			out.URL.RawQuery = q.Encode()
		}
	}
	return out
}

// errTimeoutBudgetExceeded marks a RoundTrip attempt WE aborted because the
// caller's timeout budget elapsed while still awaiting response headers.
// Deliberately distinct from context.Canceled (which classifyNetworkError
// does not credit to the breaker — that carve-out is for an ordinary client
// disconnect): this IS a provider-degradation signal, so classifyNetworkError
// falls through to its "unknown transport error => degraded" default for it.
var errTimeoutBudgetExceeded = errors.New("circuit: timeout budget exceeded while awaiting response headers")

// roundTripWithBudget runs a single RoundTrip attempt but aborts it if
// response headers have not arrived by deadline.
//
// Unlike attaching a context deadline to the request for its whole
// lifetime, this ONLY governs the wait for headers: the timer race happens
// around the RoundTrip call itself, and once RoundTrip returns (success or
// failure) the timer is stopped and never touches the response body — a
// stream that starts before the deadline keeps flowing after it. Achieving
// that requires NOT using context.WithDeadline (whose cancellation, once
// armed, cannot be selectively "disarmed only for the body"); instead a
// plain context.WithCancel is canceled explicitly, and only when the timer
// actually fires before RoundTrip has returned.
func (t *Transport) roundTripWithBudget(attemptReq *http.Request, deadline time.Time) (*http.Response, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, errTimeoutBudgetExceeded
	}

	budgetCtx, cancel := context.WithCancel(attemptReq.Context())
	defer cancel()
	attemptReq = attemptReq.WithContext(budgetCtx)

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := t.inner.RoundTrip(attemptReq)
		done <- result{resp, err}
	}()

	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case res := <-done:
		return res.resp, res.err
	case <-timer.C:
		// Headers have not arrived within budget. Cancel the underlying
		// request so the goroutine unblocks (net/http's Transport honours
		// context cancellation while dialing and while awaiting the
		// response), then wait for it so we never leak the goroutine or
		// race a later attempt against this one's connection.
		cancel()
		res := <-done
		if res.err != nil && errors.Is(res.err, context.Canceled) {
			return nil, errTimeoutBudgetExceeded
		}
		// A response raced in just as the timer fired — headers technically
		// arrived in time. Honour it rather than discarding a good response.
		return res.resp, res.err
	}
}
