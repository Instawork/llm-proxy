package middleware

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/providers"
)

const (
	costLimitReasonHeader    = "X-Cost-Limit-Reason"
	costLimitCentsHeader     = "X-Cost-Limit-Cents"
	costSpendCentsHeader     = "X-Cost-Spend-Cents"
	costLimitExceeded        = "daily_cost_limit_exceeded"
	costLimitMonthlyExceeded = "monthly_cost_limit_exceeded"
	costLimitDegraded        = "daily_cost_limit_read_degraded"

	// defaultReservationGrace is how long a reconciled reservation lingers
	// after a request completes before it is released. It must comfortably
	// exceed the async cost-tracker flush latency so the actual spend has
	// landed in the by_key hash before the reservation drops — otherwise a
	// brief window would exist where neither the reservation nor the recorded
	// spend counts (re-opening the overshoot gap). Lingering instead causes a
	// brief, harmless double-count (conservative over-enforcement).
	defaultReservationGrace = 30 * time.Second
	// defaultEstimateOutputTokens is the assumed completion length when a
	// request does not pin one, used only to size the up-front reservation.
	// The reservation is reconciled to the real cost once the response's token
	// headers are known, so this only needs to be a reasonable upper-ish bound.
	defaultEstimateOutputTokens = 1024
)

// KeySpendReader returns rolling per-key spend for the current UTC day. When a
// shared rollup store is configured the implementation returns the fleet-wide
// spend (read from Redis) so cost limits are enforced cluster-wide rather than
// per-instance. The request context bounds any backing store read.
type KeySpendReader interface {
	KeySpendUSD(ctx context.Context, maskedKeyID string) float64
}

// KeyMonthlySpendReader returns rolling per-key spend for the current UTC month.
type KeyMonthlySpendReader interface {
	KeyMonthlySpendUSD(ctx context.Context, maskedKeyID string) float64
}

// KeySpendReaderDetailed is an optional extension of KeySpendReader that also
// reports whether the spend read was degraded — i.e. a fleet rollup store is
// bound but its (Redis) read failed, so the returned spend is local-only and
// cannot see sibling instances. A reader that implements this lets the
// middleware fail closed when fleet visibility is lost.
type KeySpendReaderDetailed interface {
	KeySpendUSDDetailed(ctx context.Context, maskedKeyID string) (spendUSD float64, degraded bool)
}

// KeyMonthlySpendReaderDetailed is the monthly counterpart of KeySpendReaderDetailed.
type KeyMonthlySpendReaderDetailed interface {
	KeyMonthlySpendUSDDetailed(ctx context.Context, maskedKeyID string) (spendUSD float64, degraded bool)
}

// KeySpendReserver is an optional extension that enables synchronous,
// cluster-wide cost reservations. Reserving an estimate BEFORE a request runs
// (and reconciling it to the actual cost after) closes the check-before /
// charge-after window that otherwise lets concurrent and multi-instance
// requests overshoot a daily cap before any of them is recorded. See
// coststats.Recorder for the contract details.
type KeySpendReserver interface {
	ReserveKeySpend(ctx context.Context, maskedKeyID string, estimateUSD float64, limitCents int64) (allowed, reservationActive bool)
	AdjustKeyReservation(ctx context.Context, maskedKeyID string, deltaUSD float64)
}

// KeyMonthlySpendReserver is the monthly counterpart of KeySpendReserver.
type KeyMonthlySpendReserver interface {
	ReserveKeyMonthlySpend(ctx context.Context, maskedKeyID string, estimateUSD float64, limitCents int64) (allowed, reservationActive bool)
	AdjustKeyMonthlyReservation(ctx context.Context, maskedKeyID string, deltaUSD float64)
}

// CostEstimator returns the USD cost for a (provider, model) call with the
// given token counts. Typically backed by the cost tracker's pricing tables.
type CostEstimator func(provider, model string, inputTokens, outputTokens int) float64

// CostLimitOptions tunes cost-limit enforcement behavior.
type CostLimitOptions struct {
	// FailClosedOnReadError, when true, makes the middleware reject requests
	// (503) for cost-limited keys whenever the fleet-wide spend read is
	// degraded (rollup bound but Redis unreachable). The default (false) is
	// fail-open: enforce on the local-only view and emit a warning, trading
	// hard cluster-wide accuracy for availability during a Redis outage.
	// Only keys that actually carry a daily_cost_limit are affected either
	// way; unlimited keys always pass through.
	FailClosedOnReadError bool

	// Estimate prices a call for the up-front reservation and post-request
	// reconciliation. When nil (or when no reserver is available) the
	// middleware uses read-only enforcement instead of reservations.
	Estimate CostEstimator
	// Estimation configures request-token estimation (mirrors rate limiting).
	Estimation providers.YAMLConfigEstimationAdapter
	// ReservationGrace overrides defaultReservationGrace when > 0.
	ReservationGrace time.Duration
}

// CostLimitMiddleware blocks provider requests when an iw: key's recorded daily
// and/or monthly spend has reached its configured cost limits (cents). A limit
// of zero (or any non-positive value) means unlimited for that window.
//
// When the spend reader also supports reservations (KeySpendReserver /
// KeyMonthlySpendReserver) and a cost Estimate function is configured,
// enforcement is synchronous and cluster-wide: an estimated cost is atomically
// reserved before the request, reconciled to the actual cost afterward, and
// released after a grace period. Otherwise it falls back to reading recorded
// spend (which can briefly overshoot under concurrency / async tracking lag).
func CostLimitMiddleware(pm *providers.ProviderManager, spend KeySpendReader, opts ...CostLimitOptions) func(http.Handler) http.Handler {
	if spend == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	var opt CostLimitOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	detailed, _ := spend.(KeySpendReaderDetailed)
	monthlyReader, _ := spend.(KeyMonthlySpendReader)
	monthlyDetailed, _ := spend.(KeyMonthlySpendReaderDetailed)
	reserver, _ := spend.(KeySpendReserver)
	monthlyReserver, _ := spend.(KeyMonthlySpendReserver)
	grace := opt.ReservationGrace
	if grace <= 0 {
		grace = defaultReservationGrace
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			prov := GetProviderFromRequest(pm, r)
			if prov == nil {
				next.ServeHTTP(w, r)
				return
			}

			rec, ok := apikeys.FromContext(r.Context())
			if !ok || rec == nil || !hasCostLimit(rec) {
				next.ServeHTTP(w, r)
				return
			}

			masked := MaskKeyID(rec.PK)

			// Reservation path: synchronous, cluster-wide enforcement.
			if opt.Estimate != nil && (reserver != nil || monthlyReserver != nil) {
				if handled := tryReserveAndServe(w, r, next, prov, rec, masked, reserver, monthlyReserver, opt, grace); handled {
					return
				}
				// reservationActive was false (unbound store / backend error):
				// fall through to read-only enforcement below.
			}

			enforceReadOnly(w, r, next, prov, rec, masked, spend, detailed, monthlyReader, monthlyDetailed, opt)
		})
	}
}

func hasCostLimit(rec *apikeys.APIKey) bool {
	return rec.DailyCostLimit > 0 || rec.MonthlyCostLimit > 0
}

// tryReserveAndServe runs the reservation enforcement path. It returns true
// when it fully handled the request (blocked or served), and false when no
// fleet reservation was made (caller should fall back to read-only).
func tryReserveAndServe(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	prov providers.Provider, rec *apikeys.APIKey, masked string,
	reserver KeySpendReserver, monthlyReserver KeyMonthlySpendReserver,
	opt CostLimitOptions, grace time.Duration,
) bool {
	needDaily := rec.DailyCostLimit > 0
	needMonthly := rec.MonthlyCostLimit > 0

	estInput, model := providers.EstimateRequestTokens(r, opt.Estimation, prov)
	estimate := opt.Estimate(prov.GetName(), model, estInput, defaultEstimateOutputTokens)
	if estimate <= 0 {
		return false
	}

	var dailyReserved bool
	if needDaily {
		if reserver == nil {
			return false
		}
		allowed, active := reserver.ReserveKeySpend(r.Context(), masked, estimate, rec.DailyCostLimit)
		if !active {
			return false
		}
		if !allowed {
			writeCostBlocked(w, prov, rec, rec.DailyCostLimit, costLimitExceeded, "daily cost limit exceeded")
			return true
		}
		dailyReserved = true
	}

	if needMonthly {
		if monthlyReserver == nil {
			if dailyReserved {
				reserver.AdjustKeyReservation(context.Background(), masked, -estimate)
			}
			return false
		}
		allowed, active := monthlyReserver.ReserveKeyMonthlySpend(r.Context(), masked, estimate, rec.MonthlyCostLimit)
		if !active {
			if dailyReserved {
				reserver.AdjustKeyReservation(context.Background(), masked, -estimate)
			}
			return false
		}
		if !allowed {
			if dailyReserved {
				reserver.AdjustKeyReservation(context.Background(), masked, -estimate)
			}
			writeCostBlocked(w, prov, rec, rec.MonthlyCostLimit, costLimitMonthlyExceeded, "monthly cost limit exceeded")
			return true
		}
	}

	sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(sw, r)

	actual := reconcileActualCost(w, sw, prov.GetName(), model, estimate, opt.Estimate)
	if dailyReserved {
		reserver.AdjustKeyReservation(context.Background(), masked, actual-estimate)
		time.AfterFunc(grace, func() {
			reserver.AdjustKeyReservation(context.Background(), masked, -actual)
		})
	}
	if needMonthly && monthlyReserver != nil {
		monthlyReserver.AdjustKeyMonthlyReservation(context.Background(), masked, actual-estimate)
		time.AfterFunc(grace, func() {
			monthlyReserver.AdjustKeyMonthlyReservation(context.Background(), masked, -actual)
		})
	}
	return true
}

// reconcileActualCost computes the realized cost of a completed request from
// the token headers set by TokenParsingMiddleware. On a hard upstream failure
// (5xx) the request consumed no quota, so the cost is zero. When token counts
// are unavailable on an otherwise-successful response (e.g. a stream that did
// not surface usage), the original estimate is retained as a conservative
// stand-in so the reservation never under-counts.
func reconcileActualCost(w http.ResponseWriter, sw *statusCapturingWriter, provider, estModel string, estimate float64, estimateFn CostEstimator) float64 {
	if sw.status >= 500 {
		return 0
	}
	inTok := headerToInt(w.Header().Get("X-LLM-Input-Tokens"))
	outTok := headerToInt(w.Header().Get("X-LLM-Output-Tokens"))
	if inTok <= 0 && outTok <= 0 {
		return estimate
	}
	model := w.Header().Get("X-LLM-Model")
	if model == "" {
		model = estModel
	}
	return estimateFn(provider, model, inTok, outTok)
}

// enforceReadOnly is the original read-recorded-spend enforcement, used when
// reservations are unavailable. It can briefly overshoot under concurrency, but
// honors the fail-closed-on-degraded-read policy.
func enforceReadOnly(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	prov providers.Provider, rec *apikeys.APIKey, masked string,
	spend KeySpendReader, detailed KeySpendReaderDetailed,
	monthlyReader KeyMonthlySpendReader, monthlyDetailed KeyMonthlySpendReaderDetailed,
	opt CostLimitOptions,
) {
	if rec.DailyCostLimit > 0 {
		var spendUSD float64
		var degraded bool
		if detailed != nil {
			spendUSD, degraded = detailed.KeySpendUSDDetailed(r.Context(), masked)
		} else {
			spendUSD = spend.KeySpendUSD(r.Context(), masked)
		}
		if degraded {
			if opt.FailClosedOnReadError {
				writeCostDegraded(w, prov, rec, rec.DailyCostLimit)
				return
			}
			log.Printf("costlimit: WARN fleet spend read degraded, enforcing per-instance only provider=%s key_prefix=%s limit_cents=%d",
				prov.GetName(), prefix(rec.PK), rec.DailyCostLimit)
		}
		spendCents := int64(math.Ceil(spendUSD * 100))
		if spendCents >= rec.DailyCostLimit {
			writeCostBlockedWithSpend(w, prov, rec, rec.DailyCostLimit, spendCents, costLimitExceeded, "daily cost limit exceeded")
			return
		}
	}

	if rec.MonthlyCostLimit > 0 {
		if monthlyReader == nil {
			next.ServeHTTP(w, r)
			return
		}
		var spendUSD float64
		var degraded bool
		if monthlyDetailed != nil {
			spendUSD, degraded = monthlyDetailed.KeyMonthlySpendUSDDetailed(r.Context(), masked)
		} else {
			spendUSD = monthlyReader.KeyMonthlySpendUSD(r.Context(), masked)
		}
		if degraded {
			if opt.FailClosedOnReadError {
				writeCostDegraded(w, prov, rec, rec.MonthlyCostLimit)
				return
			}
			log.Printf("costlimit: WARN fleet monthly spend read degraded, enforcing per-instance only provider=%s key_prefix=%s limit_cents=%d",
				prov.GetName(), prefix(rec.PK), rec.MonthlyCostLimit)
		}
		spendCents := int64(math.Ceil(spendUSD * 100))
		if spendCents >= rec.MonthlyCostLimit {
			writeCostBlockedWithSpend(w, prov, rec, rec.MonthlyCostLimit, spendCents, costLimitMonthlyExceeded, "monthly cost limit exceeded")
			return
		}
	}

	next.ServeHTTP(w, r)
}

func writeCostDegraded(w http.ResponseWriter, prov providers.Provider, rec *apikeys.APIKey, limitCents int64) {
	w.Header().Set(costLimitReasonHeader, costLimitDegraded)
	log.Printf("costlimit: fail-closed (fleet spend read degraded) provider=%s key_prefix=%s limit_cents=%d",
		prov.GetName(), prefix(rec.PK), limitCents)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "cost limit temporarily unverifiable",
	})
}

func writeCostBlocked(w http.ResponseWriter, prov providers.Provider, rec *apikeys.APIKey, limitCents int64, reason, msg string) {
	w.Header().Set(costLimitReasonHeader, reason)
	w.Header().Set(costLimitCentsHeader, strconv.FormatInt(limitCents, 10))
	log.Printf("costlimit: block (reserved) provider=%s key_prefix=%s limit_cents=%d reason=%s",
		prov.GetName(), prefix(rec.PK), limitCents, reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeCostBlockedWithSpend(w http.ResponseWriter, prov providers.Provider, rec *apikeys.APIKey, limitCents, spendCents int64, reason, msg string) {
	w.Header().Set(costLimitReasonHeader, reason)
	w.Header().Set(costLimitCentsHeader, strconv.FormatInt(limitCents, 10))
	w.Header().Set(costSpendCentsHeader, strconv.FormatInt(spendCents, 10))
	log.Printf("costlimit: block provider=%s key_prefix=%s spend_cents=%d limit_cents=%d reason=%s",
		prov.GetName(), prefix(rec.PK), spendCents, limitCents, reason)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
