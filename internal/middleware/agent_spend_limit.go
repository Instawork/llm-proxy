package middleware

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/observability"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

const (
	agentSpendResetHeader         = "X-Agent-Spend-Reset"
	agentSpendLimitExceededReason = "monthly_agent_cost_limit_exceeded"
	agentSpendDegradedReason      = "monthly_agent_cost_limit_read_degraded"
	agentSpendLimitExceededMsg    = "monthly agent cost limit exceeded"

	// Metric names are relative to the sink namespace ("llm").
	agentSpendDecisionMetric = "agent_spend.decision"
	agentSpendDegradedMetric = "agent_spend.enforcement_degraded"

	agentSpendDegradedLogInterval = time.Minute
)

// Values of the decision tag on llm.agent_spend.decision.
const (
	AgentSpendDecisionAllow      = "allow"
	AgentSpendDecisionBlock      = "block"
	AgentSpendDecisionWouldBlock = "would_block"
	AgentSpendDecisionExempt     = "exempt"
	AgentSpendDecisionFailOpen   = "fail_open"
	AgentSpendDecisionFailClosed = "fail_closed"
)

// Values of the reason tag on llm.agent_spend.enforcement_degraded.
const (
	AgentSpendDegradedReserveError     = "reserve_error"
	AgentSpendDegradedReadError        = "read_error"
	AgentSpendDegradedLimitLookupError = "limit_lookup_error"
	AgentSpendDegradedReconcileError   = "reconcile_error"
	AgentSpendDegradedStoreUnbound     = "store_unbound"
)

// AgentSpendStore is the fleet-wide monthly per-user counter, implemented by
// *coststats.Recorder. Every call takes the month explicitly so a request
// reserves, reconciles, and releases in the month it started in.
type AgentSpendStore interface {
	UserMonthlySpendUSDDetailed(ctx context.Context, month, userID string) (spendUSD float64, degraded bool)
	ReserveUserMonthlySpend(ctx context.Context, month, userID string, estimateUSD float64, limitCents int64) (allowed, reservationActive bool, err error)
	AdjustUserMonthlyReservation(ctx context.Context, month, userID string, deltaUSD float64) error
}

// AgentLimitSource resolves a label's effective limits, implemented by
// *apikeys.AgentLimitResolver.
type AgentLimitSource interface {
	Resolve(ctx context.Context, label string) (apikeys.EffectiveAgentLimits, error)
}

// AgentSpendLimitOptions configures AgentSpendLimitMiddleware.
type AgentSpendLimitOptions struct {
	Config config.AgentSpendLimitsConfig
	Limits AgentLimitSource
	Spend  AgentSpendStore
	// Estimate prices the up-front reservation and the reconcile. When nil,
	// or when a model is unpriced, the middleware compares recorded spend
	// only and reserves nothing.
	Estimate   CostEstimator
	Estimation providers.YAMLConfigEstimationAdapter
	// ReservationGrace overrides defaultReservationGrace when > 0.
	ReservationGrace time.Duration
	Metrics          observability.MetricsSink
	// Now overrides time.Now in tests.
	Now func() time.Time
}

// AgentSpendLimitMiddleware caps calendar-month (UTC) spend per agent user
// label, whatever credential the request carries. The label comes only from
// the /meta/{label}/ path or the X-User-ID header, and is governed only when
// it carries the configured prefix.
//
// In enforce mode a governed call at or over its hard limit gets a 402; in
// observe mode the would-block is logged and counted and the call is served.
// Exempt labels skip the reservation; their spend is still recorded by the
// cost tracker. When the limit lookup or the shared counter fails, the call
// is served (unless fail_closed_on_read_error is set in enforce mode) and
// llm.agent_spend.enforcement_degraded is incremented.
//
// It must never mutate the body, path, or headers of a request, because
// SigV4 clients sign them: it only reads the memoized body for the token
// estimate and sets headers on its own 402 and 503 responses.
func AgentSpendLimitMiddleware(pm *providers.ProviderManager, opt AgentSpendLimitOptions) func(http.Handler) http.Handler {
	if !opt.Config.Enabled || opt.Spend == nil || opt.Limits == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if opt.Metrics == nil {
		opt.Metrics = observability.NoopSink
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.ReservationGrace <= 0 {
		opt.ReservationGrace = defaultReservationGrace
	}
	m := &agentSpendLimiter{opt: opt, degradedLog: newDegradedLogLimiter(agentSpendDegradedLogInterval, opt.Now)}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prov := GetProviderFromRequest(pm, r)
			if prov == nil {
				next.ServeHTTP(w, r)
				return
			}
			label := agentSpendLabel(r)
			if !m.governs(label) {
				next.ServeHTTP(w, r)
				return
			}
			m.serve(w, r, next, prov, label)
		})
	}
}

// agentSpendLabel returns the request's user label from the /meta/ path
// context or, failing that, X-User-ID. This is the same precedence as
// ExtractUserIDFromRequest, so the label matches the one the cost tracker
// records spend under; the body, query, token, and IP fallbacks are ignored.
func agentSpendLabel(r *http.Request) string {
	if v, ok := r.Context().Value(userIDContextKey).(string); ok && v != "" {
		return v
	}
	return r.Header.Get("X-User-ID")
}

type agentSpendLimiter struct {
	opt         AgentSpendLimitOptions
	degradedLog *degradedLogLimiter
}

// governs also requires a label the monthly counter can store, since spend
// for any other label is never recorded.
func (m *agentSpendLimiter) governs(label string) bool {
	return m.opt.Config.Governs(label) && apikeys.ValidateAgentLabel(label) == nil
}

func (m *agentSpendLimiter) mode() string {
	if m.opt.Config.Enforcing() {
		return config.AgentSpendModeEnforce
	}
	return config.AgentSpendModeObserve
}

func (m *agentSpendLimiter) serve(w http.ResponseWriter, r *http.Request, next http.Handler, prov providers.Provider, label string) {
	now := m.opt.Now().UTC()
	month := now.Format("2006-01")

	limits, err := m.opt.Limits.Resolve(r.Context(), label)
	if err != nil {
		m.degraded(w, r, next, label, AgentSpendDegradedLimitLookupError, limits.HardMonthlyCents, now)
		return
	}
	if limits.Exempt {
		m.decision(AgentSpendDecisionExempt, label)
		next.ServeHTTP(w, r)
		return
	}
	hard := limits.HardMonthlyCents

	var estimate float64
	var model string
	if m.opt.Estimate != nil && hard > 0 {
		var estInput int
		estInput, model = providers.EstimateRequestTokens(r, m.opt.Estimation, prov)
		estimate = m.opt.Estimate(prov.GetName(), model, estInput, 0, 0, defaultEstimateOutputTokens)
	}
	if estimate <= 0 {
		m.serveReadOnly(w, r, next, prov, label, month, hard, now)
		return
	}

	allowed, active, err := m.opt.Spend.ReserveUserMonthlySpend(r.Context(), month, label, estimate, hard)
	switch {
	case err != nil:
		m.degraded(w, r, next, label, AgentSpendDegradedReserveError, hard, now)
		return
	case !active:
		m.degraded(w, r, next, label, AgentSpendDegradedStoreUnbound, hard, now)
		return
	case !allowed:
		spendUSD, _ := m.opt.Spend.UserMonthlySpendUSDDetailed(r.Context(), month, label)
		m.overLimit(w, r, next, prov, label, hard, int64(math.Ceil(spendUSD*100)), now)
		return
	}

	m.decision(AgentSpendDecisionAllow, label)
	sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
	// Deferred so the reservation is settled even when the handler panics
	// (http.ErrAbortHandler on a client disconnect mid-stream).
	defer m.settle(w, sw, prov, model, label, month, estimate)
	next.ServeHTTP(sw, r)
}

// settle reconciles the reservation to the actual cost once the response,
// including a stream, has finished, then releases it after the grace period
// so the recorded spend has landed. A 5xx releases it at once.
func (m *agentSpendLimiter) settle(w http.ResponseWriter, sw *statusCapturingWriter, prov providers.Provider, model, label, month string, estimate float64) {
	actual := reconcileActualCost(w, sw, prov.GetName(), model, estimate, m.opt.Estimate)
	if err := m.opt.Spend.AdjustUserMonthlyReservation(context.Background(), month, label, actual-estimate); err != nil {
		m.reportDegraded(AgentSpendDegradedReconcileError, false)
	}
	if actual <= 0 {
		return
	}
	time.AfterFunc(m.opt.ReservationGrace, func() {
		if err := m.opt.Spend.AdjustUserMonthlyReservation(context.Background(), month, label, -actual); err != nil {
			m.reportDegraded(AgentSpendDegradedReconcileError, false)
		}
	})
}

// serveReadOnly compares recorded spend with the hard limit when no
// reservation can be made: the model is unpriced, no estimator is set, or
// the hard limit is 0 (which blocks every call).
func (m *agentSpendLimiter) serveReadOnly(w http.ResponseWriter, r *http.Request, next http.Handler, prov providers.Provider, label, month string, hard int64, now time.Time) {
	spendUSD, degraded := m.opt.Spend.UserMonthlySpendUSDDetailed(r.Context(), month, label)
	if degraded {
		m.degraded(w, r, next, label, AgentSpendDegradedReadError, hard, now)
		return
	}
	spendCents := int64(math.Ceil(spendUSD * 100))
	if spendCents >= hard {
		m.overLimit(w, r, next, prov, label, hard, spendCents, now)
		return
	}
	m.decision(AgentSpendDecisionAllow, label)
	next.ServeHTTP(w, r)
}

// overLimit returns the 402 in enforce mode and serves the call in observe
// mode. Nothing is reserved on this path, so there is nothing to release.
func (m *agentSpendLimiter) overLimit(w http.ResponseWriter, r *http.Request, next http.Handler, prov providers.Provider, label string, limitCents, spendCents int64, now time.Time) {
	if !m.opt.Config.Enforcing() {
		m.decision(AgentSpendDecisionWouldBlock, label)
		proxylog.Proxy("agentspend: would block (observe) provider=%s label=%s spend_cents=%d limit_cents=%d",
			prov.GetName(), label, spendCents, limitCents)
		next.ServeHTTP(w, r)
		return
	}
	m.decision(AgentSpendDecisionBlock, label)
	proxylog.Proxy("agentspend: block provider=%s label=%s spend_cents=%d limit_cents=%d",
		prov.GetName(), label, spendCents, limitCents)
	h := w.Header()
	h.Set(costLimitReasonHeader, agentSpendLimitExceededReason)
	h.Set(costLimitCentsHeader, strconv.FormatInt(limitCents, 10))
	h.Set(costSpendCentsHeader, strconv.FormatInt(spendCents, 10))
	h.Set(agentSpendResetHeader, nextMonthStartUTC(now).Format(time.RFC3339))
	proxylog.WriteProxyJSONError(w, http.StatusPaymentRequired, agentSpendLimitExceededMsg)
}

// degraded handles a failed limit lookup or counter call: it reports the
// reason and serves the call, or returns 503 when fail_closed_on_read_error
// is set in enforce mode.
func (m *agentSpendLimiter) degraded(w http.ResponseWriter, r *http.Request, next http.Handler, label, reason string, limitCents int64, now time.Time) {
	failClosed := m.opt.Config.FailClosedOnReadError && m.opt.Config.Enforcing()
	m.reportDegraded(reason, failClosed)
	if failClosed {
		m.decision(AgentSpendDecisionFailClosed, label)
		h := w.Header()
		h.Set(costLimitReasonHeader, agentSpendDegradedReason)
		if limitCents > 0 {
			h.Set(costLimitCentsHeader, strconv.FormatInt(limitCents, 10))
		}
		h.Set(agentSpendResetHeader, nextMonthStartUTC(now).Format(time.RFC3339))
		proxylog.WriteProxyJSONError(w, http.StatusServiceUnavailable, "monthly agent cost limit temporarily unverifiable")
		return
	}
	m.decision(AgentSpendDecisionFailOpen, label)
	next.ServeHTTP(w, r)
}

func (m *agentSpendLimiter) decision(decision, label string) {
	_ = m.opt.Metrics.Incr(agentSpendDecisionMetric, []string{
		"decision:" + decision,
		"mode:" + m.mode(),
		"label:" + label,
	}, 1)
}

// reportDegraded increments the degraded metric and writes at most one log
// line per reason per interval. Neither carries the label or the error,
// which can embed it.
func (m *agentSpendLimiter) reportDegraded(reason string, failClosed bool) {
	_ = m.opt.Metrics.Incr(agentSpendDegradedMetric, []string{
		"reason:" + reason,
		"mode:" + m.mode(),
	}, 1)
	if n, ok := m.degradedLog.allow(reason); ok {
		action := "failing open"
		if failClosed {
			action = "failing closed"
		}
		proxylog.Proxy("agentspend: enforcement degraded, %s reason=%s count_since_last=%d", action, reason, n)
	}
}

// nextMonthStartUTC returns 00:00 UTC on the first day of the month after t.
func nextMonthStartUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
}

// degradedLogLimiter allows one log line per reason per interval and counts
// the events in between.
type degradedLogLimiter struct {
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	last    map[string]time.Time
	pending map[string]int64
}

func newDegradedLogLimiter(interval time.Duration, now func() time.Time) *degradedLogLimiter {
	return &degradedLogLimiter{
		interval: interval,
		now:      now,
		last:     make(map[string]time.Time),
		pending:  make(map[string]int64),
	}
}

// allow records one event for reason and reports whether to log it, with the
// number of events since the previous logged line (including this one).
func (l *degradedLogLimiter) allow(reason string) (int64, bool) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pending[reason]++
	if last, ok := l.last[reason]; ok && now.Sub(last) < l.interval {
		return 0, false
	}
	n := l.pending[reason]
	l.last[reason] = now
	l.pending[reason] = 0
	return n, true
}
