package circuit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// errRetryBodyTooLarge is an internal sentinel returned by cacheBody when
// the incoming request body exceeds Config.MaxRetryableBodyBytes.  We
// treat it as "body too large to buffer — fall through and disable
// retries" rather than a hard error back to the caller, so oversize
// requests still reach the upstream on a best-effort basis.
var errRetryBodyTooLarge = errors.New("circuit: request body exceeds MaxRetryableBodyBytes")

// retryAttemptKey is the context key used to pass the current attempt index
// down to the inner transport (used by the test-mode transport).
type retryAttemptKey struct{}

// Transport wraps an inner http.RoundTripper with circuit-breaker logic:
//
//   - checks the circuit state before every logical request;
//   - retries transient (degraded-class) failures up to MaxTransientRetries;
//   - retries rate-limit failures up to MaxRateLimitRetries with Retry-After
//     backoff;
//   - records terminal failures in the Store and opens the circuit when the
//     threshold is crossed;
//   - performs a single probe request when the circuit is Half-Open;
//   - injects Config.DegradedSignal into every synthesised degraded response
//     body so downstream clients can reliably detect provider degradation
//     even after SDK / framework exception wrapping hides headers and status
//     codes (see DefaultDegradedSignal for the full rationale).
//
// # Per-key state-machine scoping
//
// Historically the breaker tracked one state machine per provider.  Since
// per-model keying landed it tracks one per `<provider>:<model>` key,
// gracefully falling back to bare `<provider>` when the model cannot be
// extracted.  A separate per-provider rollup signal (opt-in via
// PerProviderRollupThreshold) escalates a wholesale outage when many
// distinct model breakers trip concurrently — see Transport.effectiveState
// for the join.
type Transport struct {
	inner    http.RoundTripper
	store    Store
	cfg      Config
	provider string
	log      *slog.Logger
	metrics  MetricsSink
	modelFn  ModelFromRequestFunc
	callerFn CallerFromRequestFunc
	activity ActivityRecorder
}

// NewTransport wraps inner with circuit-breaker behaviour for provider.
//
// Optional behaviour (dogstatsd metric emission, model-name
// extraction) is configured via Option values; see WithMetrics and
// WithModelExtractor.  Callers that pass no options get a transport
// that logs but does not emit metrics — which preserves the previous
// behaviour exactly.
func NewTransport(inner http.RoundTripper, store Store, cfg Config, provider string, log *slog.Logger, opts ...Option) *Transport {
	if log == nil {
		log = slog.Default()
	}
	t := &Transport{
		inner:    inner,
		store:    store,
		cfg:      cfg.Defaults(),
		provider: provider,
		log:      log,
		metrics:  noopMetrics{},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// maxErrorStringLength bounds how much of an upstream / network error we
// embed in slog attributes and metric tags.  Long errors (e.g. wrapped
// TLS chains) blow up log volume and cardinality if untrimmed.
const maxErrorStringLength = 256

// failureContext is the shared, pre-computed payload that every
// circuit-breaker observability call site uses.  Building it once per
// event (and reusing it for both the slog line and the dogstatsd
// counter) means we parse the request body for model attribution at
// most once per failure even when the same event also crosses a state
// transition or a "threshold crossed" warn line.
//
// Field set is deliberately flat so Datadog can facet on each
// attribute and so the metric tag set stays low-cardinality (model is
// bounded by the upstream catalogue, kind by the FailureKind enum,
// status_code by HTTP).
//
// CBKey is the actual breaker key the Store sees (`<provider>:<model>` or
// bare `<provider>` fallback) so operators can correlate a log line to a
// specific Redis hash.  It always equals provider + ":" + model when both
// are set; we precompute it once instead of inferring it at log time.
//
// UpstreamError carries a compact parse of the upstream JSON error body
// (e.g. Gemini status=UNAVAILABLE, OpenAI code=insufficient_quota).
// Populated before the response body is drained; omitted from metric tags
// because message text is high-cardinality.
type failureContext struct {
	Provider      string
	Model         string
	CBKey         string
	Caller        string
	Path          string
	Method        string
	StatusCode    int
	Kind          FailureKind
	ErrorString   string
	UpstreamError string
}

// newFailureContext builds the canonical context for a failure event.
// Pass nil resp / nil err for synthetic events (fast-fail, probe slot
// taken) and follow up with .withKind(KindCircuitOpen) so the kind
// reflects "we deliberately didn't try" rather than "upstream returned
// nothing".
func (t *Transport) newFailureContext(req *http.Request, resp *http.Response, err error) failureContext {
	fc := failureContext{
		Provider:    t.provider,
		Kind:        ClassifyFailureKind(t.provider, resp, err),
		ErrorString: truncateError(err),
	}
	if req != nil {
		if req.URL != nil {
			fc.Path = req.URL.Path
		}
		fc.Method = req.Method
	}
	if resp != nil {
		fc.StatusCode = resp.StatusCode
	}
	if t.modelFn != nil && req != nil && req.GetBody != nil {
		// Best-effort: only call the extractor when the request body is
		// already replayable.  Synthetic fast-fail paths run before cacheBody,
		// and allowing extractors to fall back to req.Body there can turn an
		// open circuit into an unbounded body read just for observability.
		fc.Model = t.modelFn(req)
	}
	if t.callerFn != nil && req != nil {
		// Best-effort caller label (e.g. proxy API-key description). Reads
		// from the request context only — no body access — so it is safe on
		// synthetic fast-fail paths that run before cacheBody.
		fc.Caller = t.callerFn(req)
	}
	fc.CBKey = composeKey(fc.Provider, fc.Model)
	return fc
}

// keyFor returns the breaker key for req.  Per-model keying is automatic
// when the configured ModelFromRequestFunc successfully extracts a model
// from the (already cached) request body; we fall back to the bare
// provider name when the extractor is missing, the body is oversize, or
// the model field is absent.
//
// The fallback exists for two reasons:
//
//  1. Pre-cacheBody call sites (e.g. the initial GetState check that
//     happens before runWithRetries even touches the body) cannot
//     replay a streaming body to extract the model without breaking
//     the upstream forward.
//  2. Oversize-body requests (Config.MaxRetryableBodyBytes overflow)
//     never get a usable GetBody, so the extractor cannot run.
//
// Per-provider keying for those edge cases is the safe default: a
// genuine wholesale outage will trip the per-provider key just like
// the pre-per-model behaviour did, so worst case we degrade back to
// the v1 keying granularity instead of silently masking the breaker.
func (t *Transport) keyFor(req *http.Request) string {
	if t.modelFn == nil || req == nil || req.GetBody == nil {
		return t.provider
	}
	model := t.modelFn(req)
	if model == "" {
		// Body IS buffered but no model could be extracted (malformed or
		// non-chat payload). Do NOT fold these into the bare-provider key:
		// that key doubles as the provider-wide forced-open overlay
		// (insufficient_quota) and is joined into every per-model request's
		// effective state. Letting organic unextractable-request failures
		// open it would fast-fail ALL healthy per-model traffic — a cheap
		// DoS. Isolate them in a dedicated sentinel breaker instead.
		return composeKey(t.provider, unknownModelKey)
	}
	return composeKey(t.provider, model)
}

// unknownModelKey is the model component used for requests whose body is
// buffered but yields no extractable model. Keeps such requests in their own
// per-provider breaker, isolated from both real per-model keys and the
// bare-provider forced-open overlay.
const unknownModelKey = "_unknown"

// composeKey is the canonical (provider, model) → store key formatter.
// Centralised so log lines, metric tags, and Store calls cannot drift.
func composeKey(provider, model string) string {
	if model == "" {
		return provider
	}
	return provider + ":" + model
}

// withKind returns a copy of fc with the failure kind overridden.
// Used by synthetic-event call sites (fast-fail, probe slot taken,
// would-have-fast-failed, body too large) where the default
// classification (which keys off resp/err that don't exist) is
// misleading.
func (fc failureContext) withKind(k FailureKind) failureContext {
	fc.Kind = k
	return fc
}

func (fc failureContext) withUpstreamError(detail string) failureContext {
	fc.UpstreamError = truncateString(detail)
	return fc
}

// enrichedFailureContext builds failureContext and attaches an upstream
// error summary.  When capturedDetail is empty and resp.Body is still
// readable, the detail is peeked from the response body.
func (t *Transport) enrichedFailureContext(req *http.Request, resp *http.Response, err error, capturedDetail string) failureContext {
	fc := t.newFailureContext(req, resp, err)
	if capturedDetail != "" {
		fc = fc.withUpstreamError(capturedDetail)
		fc.Kind = refineFailureKindFromUpstreamDetail(t.provider, capturedDetail, fc.Kind)
		return fc
	}
	if err == nil && resp != nil && resp.StatusCode >= 400 && resp.Body != nil {
		if detail := peekUpstreamErrorDetail(t.provider, resp); detail != "" {
			fc = fc.withUpstreamError(detail)
			fc.Kind = refineFailureKindFromUpstreamDetail(t.provider, detail, fc.Kind)
		}
	}
	return fc
}

// attrs returns a slog-friendly attribute slice with a stable schema.
// The exact field set is documented on failureContext above.
func (fc failureContext) attrs() []any {
	attrs := []any{
		"provider", fc.Provider,
		"model", fc.Model,
		"cb_key", fc.CBKey,
		"caller", fc.Caller,
		"path", fc.Path,
		"method", fc.Method,
		"status_code", fc.StatusCode,
		"failure_kind", string(fc.Kind),
		"error", fc.ErrorString,
	}
	if fc.UpstreamError != "" {
		attrs = append(attrs, "upstream_error", fc.UpstreamError)
	}
	return attrs
}

// metricTags returns the dogstatsd tag set matching attrs().
//
// Empty model is rewritten to "unknown" because Datadog silently drops
// tags with empty values, which would offset the rest of the tag list
// and break facet filters.  All other empty values are
// preserved (status_code:0 for transport errors, etc.).
//
// cb_key is intentionally omitted from the metric tag set: it is
// derivable from `provider:model` via concatenation and including it
// would double-count cardinality without adding any new dimension.
//
// caller is the API-key description (a human label like "finch-prod"),
// so operators can attribute degraded responses to a downstream client.
// Its cardinality is bounded by the number of provisioned proxy keys —
// low and operator-controlled — and is length-capped via
// normalizeTagValue as defence-in-depth; empty becomes "unknown".
//
// Tag values are length-capped via normalizeTagValue to prevent
// pathological model IDs (e.g. an attacker passing a 4096-char string
// or a one-off fine-tune SKU) from blowing up the Datadog cardinality
// budget. Datadog's per-metric tag-value cardinality limit is 1k by
// default; without a cap a single noisy caller can exhaust that budget
// and start silently dropping metric points across the fleet.
func (fc failureContext) metricTags() []string {
	return []string{
		"provider:" + normalizeTagValue(fc.Provider),
		"model:" + normalizeTagValue(fc.Model),
		"caller:" + normalizeTagValue(fc.Caller),
		"status_code:" + strconv.Itoa(fc.StatusCode),
		"failure_kind:" + normalizeTagValue(string(fc.Kind)),
	}
}

// normalizeTagValue returns a Datadog-safe form of the supplied raw tag
// value: empty becomes "unknown", and anything over 200 bytes is
// truncated to that length to keep cardinality bounded. Returning the
// truncated form (instead of dropping the tag entirely) preserves the
// shape of the tag list so downstream facet filters do not silently
// misalign indices.
func normalizeTagValue(v string) string {
	if v == "" {
		return "unknown"
	}
	const maxTagValueLen = 200
	if len(v) > maxTagValueLen {
		return v[:maxTagValueLen]
	}
	return v
}

// emit publishes a circuit.<event> counter using the supplied context.
// We deliberately ignore any dogstatsd error: UDP packet loss or an
// Agent restart must never affect request flow.  The cost-tracking
// transport follows the same convention (see internal/cost/datadog.go).
func (t *Transport) emit(event string, fc failureContext) {
	if t.metrics == nil {
		return
	}
	_ = t.metrics.Incr("circuit."+event, fc.metricTags(), 1.0)
}

// drainResponseBody reads the body to EOF and closes it so the
// HTTP/1.x connection is returned to the keep-alive pool cleanly
// when the caller is about to retry or replace the response.  Errors
// are intentionally ignored: a half-drained body is not worse than
// the alternative of leaking the connection, and any underlying
// transport error has already surfaced via the parent RoundTrip
// path.  resp may be nil — the no-op makes the helper safe to call
// unconditionally from error branches.
func drainResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// truncateError returns a slog-friendly truncation of err for use in
// the "error" attribute of failure log lines.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > maxErrorStringLength {
		return s[:maxErrorStringLength] + "...(truncated)"
	}
	return s
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// ── Observe-only / log-mode fast path ─────────────────────────────────
	// In ModeLog we intentionally skip the retry loop, fast-fail, and
	// synthetic-response machinery entirely.  We just do one round trip,
	// classify the result, record failures so /health and Redis counters
	// are accurate, emit counterfactual logs, and hand the real response
	// back to the caller.  This lets us roll out the feature to prod
	// without any behavioural change to traffic.
	//
	// Bypass is irrelevant in ModeLog (the request always passes through
	// unmodified) so we do not branch on it here; the bypass headers are
	// stripped inside runObserveOnly so they don't leak upstream.
	if t.cfg.Mode == ModeLog {
		return t.runObserveOnly(req)
	}

	// ── Test-mode: force_degraded fast path ───────────────────────────────
	// force_transient_recover is handled inside runWithRetries so it can
	// interact with the retry loop's attempt counter.
	//
	// Test-mode honouring is gated on Config.TestModeEnabled so a prod
	// deployment cannot be tricked into emitting synthetic degraded
	// responses by a client setting X-LLM-Proxy-Test-Mode or
	// llm_proxy_test_mode.  Plumbed from the CLI's three-condition
	// security gate in cmd/llm-proxy/main.go.
	if t.testModeFromRequest(req) == TestModeForceDegraded {
		t.log.Info("circuit: test-mode force_degraded",
			t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen).attrs()...)
		return t.degradedResponse(req), nil
	}

	// ── Bypass header / query param ───────────────────────────────────────
	// Callers that have no fallback wired up can opt out of fast-fail by
	// setting X-LLM-Proxy-Bypass-Circuit (or the matching query param).
	// Bypass requests still feed observability — failures we observe
	// during a bypass are credited to the breaker so dashboards stay
	// accurate — but the breaker never short-circuits the request and
	// never returns a synthetic 503.  The caller gets the real upstream
	// response, whatever it is.  See the BypassHeader docstring for the
	// full rationale.
	if t.cfg.BypassAllowed {
		if reason, ok := bypassRequested(req); ok {
			return t.runBypass(req, reason)
		}
	}

	// ── Circuit state check ───────────────────────────────────────────────
	state, key := t.effectiveStateForRequest(req)
	if t.activity != nil {
		t.activity.RecordCheck()
	}

	switch state {
	case StateOpen:
		fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Warn("circuit: fast-fail (circuit open)",
			append(fc.attrs(), "mode", ModeEnforce)...)
		t.emit("fast_fail", fc)
		if t.activity != nil {
			t.activity.RecordFastFail(t.provider, key)
		}
		return t.degradedResponse(req), nil

	case StateHalfOpen:
		return t.runProbe(req, key)

	default: // StateClosed — normal path
		return t.runWithRetries(req)
	}
}

// effectiveStateForRequest computes the breaker state to use for routing
// req.  It joins the per-key state with the optional per-provider rollup
// signal: if either says Open, the request is treated as Open.  Returns
// the resolved State plus the per-key string used (so the caller can
// thread it into runProbe / probe-slot acquisition).
//
// Rationale for joining at this layer (rather than inside the Store):
// the rollup is a separate state machine with its own threshold and
// window, and it would be a category error to encode it as part of the
// per-key state value.  Keeping the join here also means tests that
// stub out a Store get clean, orthogonal coverage of each axis.
func (t *Transport) effectiveStateForRequest(req *http.Request) (State, string) {
	ctx := req.Context()
	key := t.keyFor(req)

	state := StateClosed
	// If req.GetBody is nil, we haven't buffered the body yet, so the
	// model extractor likely returned "" and keyFor fell back to
	// t.provider.  We MUST NOT check the bare-provider state here,
	// because if it happens to be Open, we would fast-fail a request
	// that might actually belong to a perfectly healthy per-model key!
	// We defer the per-key state check to runWithRetries (after
	// cacheBody runs) in this specific case.
	if key != t.provider || req.GetBody != nil {
		var err error
		state, err = t.store.GetState(ctx, key)
		if err != nil {
			t.log.Warn("circuit: GetState error, treating as closed", "key", key, "error", err)
			state = StateClosed
		}
	} else {
		// We also defer the rollup join until after cacheBody in this
		// case.  Otherwise a provider-level rollup would fast-fail a
		// request before we know whether its actual per-model breaker is
		// HalfOpen, preventing the successful probes that clear keys from
		// the rollup window.
		return StateClosed, key
	}

	// Rollup join: only meaningful when the per-key state is Closed (an
	// already-Open per-key state takes precedence and there is nothing
	// the rollup can add).  HalfOpen requests should be allowed through
	// as probes — the rollup fast-failing them would prevent recovery,
	// which is exactly the opposite of what we want.
	//
	// Provider-wide forced-open overlay (e.g. OpenAI insufficient_quota): a
	// single account-wide failure opens the BARE-provider breaker via
	// Store.ForceOpen.  Join it here so every per-model request fast-fails,
	// and — crucially — route its half-open probe through the provider key so
	// recovery rides the normal single-probe lifecycle (after cooldown one
	// request re-tests the upstream; success closes it, failure re-opens it).
	if state == StateClosed && key != t.provider {
		if pState, err := t.store.GetState(ctx, t.provider); err == nil {
			switch pState {
			case StateOpen:
				return StateOpen, t.provider
			case StateHalfOpen:
				return StateHalfOpen, t.provider
			}
		}
	}

	if state == StateClosed && t.cfg.PerProviderRollupThreshold > 0 {
		if rec, ok := t.store.(RollupRecorder); ok {
			open, count, _ := rec.RollupOpen(
				ctx,
				t.provider,
				t.cfg.PerProviderRollupThreshold,
				t.cfg.PerProviderRollupWindowSeconds,
			)
			if open {
				t.log.Warn(
					"circuit: provider rollup open, treating per-key state as Open",
					"provider", t.provider,
					"cb_key", key,
					"rollup_threshold", t.cfg.PerProviderRollupThreshold,
					"rollup_count", count,
					"rollup_window_seconds", t.cfg.PerProviderRollupWindowSeconds,
				)
				state = StateOpen
			}
		}
	}
	return state, key
}

// bypassRequested reports whether the caller asked to bypass the breaker
// for this request, and returns the (truncated) reason string from
// BypassReasonHeader / BypassQueryParamReason.  The header takes
// precedence over the query parameter so SDKs that set BOTH (typically
// during a debug session) get deterministic behaviour.
//
// Honoured truthy values are 1, true, yes (case-insensitive).  Anything
// else — including the literal string "0" — is treated as "no bypass",
// so a misconfigured client that hard-codes the header but leaves the
// value empty cannot accidentally bypass production.
//
// The returned reason is RAW (not yet length-capped or
// allowlist-normalised) — callers that emit it as a metric tag must
// route it through Transport.normalizeBypassReason first.
func bypassRequested(req *http.Request) (reason string, ok bool) {
	if req == nil {
		return "", false
	}
	val := req.Header.Get(BypassHeader)
	if val == "" && req.URL != nil {
		val = req.URL.Query().Get(BypassQueryParam)
	}
	if !isTruthy(val) {
		return "", false
	}
	reason = req.Header.Get(BypassReasonHeader)
	if reason == "" && req.URL != nil {
		reason = req.URL.Query().Get(BypassQueryParamReason)
	}
	if len(reason) > maxBypassReasonLength {
		reason = reason[:maxBypassReasonLength]
	}
	return reason, true
}

// normalizeBypassReason converts a caller-supplied bypass reason into a
// metric-tag-safe canonical string.  Steps:
//
//  1. Empty / whitespace-only → BypassReasonUnspecified.
//  2. Lowercased and stripped of any character outside [a-z0-9_-]; runs
//     of stripped characters collapse to a single underscore.  This
//     keeps the dogstatsd tag value within the agent's allowed grammar
//     and prevents a malicious caller from injecting tag separators.
//  3. If Config.BypassReasonAllowlist is non-empty AND the normalised
//     reason is not in it, the result is BypassReasonOther.  Operators
//     who care about cardinality bounds opt in to this filter; the
//     default empty allowlist means "accept any well-formed reason".
//
// The function is intentionally on the Transport (not a free function)
// so it can read the per-instance allowlist without threading it
// through the call chain.
func (t *Transport) normalizeBypassReason(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return BypassReasonUnspecified
	}
	var b strings.Builder
	b.Grow(len(trimmed))
	prevUnderscore := false
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	normalised := strings.Trim(b.String(), "_-")
	if normalised == "" {
		return BypassReasonUnspecified
	}
	if len(t.cfg.BypassReasonAllowlist) == 0 {
		return normalised
	}
	for _, allowed := range t.cfg.BypassReasonAllowlist {
		if strings.EqualFold(allowed, normalised) {
			return normalised
		}
	}
	return BypassReasonOther
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// hasBypassMarkers returns true if the request contains ANY bypass header
// or query parameter, regardless of whether the value is truthy.
func hasBypassMarkers(req *http.Request) bool {
	if req == nil {
		return false
	}
	if req.Header.Get(BypassHeader) != "" || req.Header.Get(BypassReasonHeader) != "" {
		return true
	}
	if req.URL != nil {
		q := req.URL.Query()
		return q.Has(BypassQueryParam) || q.Has(BypassQueryParamReason)
	}
	return false
}

// stripBypassMarkers removes bypass header(s) and query param(s) from a
// request before forwarding upstream so the bypass signal is purely
// proxy-internal and never leaks into provider request logs.  Callers
// pass the result of req.Clone(ctx) so we never mutate the caller's
// request object.
func stripBypassMarkers(req *http.Request) *http.Request {
	if req == nil {
		return req
	}
	out := req.Clone(req.Context())
	out.Header.Del(BypassHeader)
	out.Header.Del(BypassReasonHeader)
	if out.URL != nil {
		q := out.URL.Query()
		hadBypass := q.Has(BypassQueryParam) || q.Has(BypassQueryParamReason)
		if hadBypass {
			q.Del(BypassQueryParam)
			q.Del(BypassQueryParamReason)
			out.URL.RawQuery = q.Encode()
		}
	}
	return out
}

// runObserveOnly performs a single pass-through RoundTrip, records observed
// failures + emits counterfactual log lines, and returns the real upstream
// response.  No retries, no synthetic responses, no fast-fail on open
// circuit — this is the "shadow" rollout path (ModeLog).
//
// Even though we never replay the body in this mode, we still call
// cacheBody so the configured ModelFromRequestFunc has something to
// read on the failure path.  The cost matches what runWithRetries
// already pays for retry support, and an oversize body falls through
// to a streaming pass-through exactly like there.
func (t *Transport) runObserveOnly(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	if err := t.cacheBody(req); err != nil {
		if !errors.Is(err, errRetryBodyTooLarge) {
			// Hard cacheBody error: req.Body has been read and closed,
			// so we can't safely fall through to the inner transport.
			// Surface the error to the caller — this matches the
			// runWithRetries behaviour and is far better than silently
			// dispatching a half-consumed body.
			return nil, fmt.Errorf("circuit: cacheBody (log-mode): %w", err)
		}
		// Oversize body: req.Body has been rewired to a streaming
		// pass-through, GetBody is nil, model extraction will return
		// "" — that's acceptable for log mode.
		t.log.Warn(
			"circuit: log-mode request body exceeds MaxRetryableBodyBytes, model attribution unavailable",
			"provider", t.provider,
			"path", req.URL.Path,
			"content_length", req.ContentLength,
			"max_retryable_body_bytes", t.cfg.MaxRetryableBodyBytes,
			"failure_kind", string(KindBodyTooLarge),
		)
	}

	key := t.keyFor(req)

	if t.activity != nil {
		t.activity.RecordCheck()
	}

	// Log what ModeEnforce *would* have done given the current state, but
	// always let the real request through.
	if state, err := t.store.GetState(ctx, key); err == nil && state == StateOpen {
		fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Info("circuit: log-mode would_have_fast_failed (circuit open, passing through)", fc.attrs()...)
		t.emit("would_have_fast_failed", fc)
	} else if t.cfg.PerProviderRollupThreshold > 0 {
		if rec, ok := t.store.(RollupRecorder); ok {
			if open, count, _ := rec.RollupOpen(ctx, t.provider,
				t.cfg.PerProviderRollupThreshold,
				t.cfg.PerProviderRollupWindowSeconds); open {
				fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
				t.log.Info("circuit: log-mode would_have_fast_failed (rollup open, passing through)",
					append(
						fc.attrs(),
						"rollup_count", count,
						"rollup_threshold", t.cfg.PerProviderRollupThreshold,
					)...)
				t.emit("would_have_fast_failed", fc)
			}
		}
	}

	// Strip bypass markers in log mode too — even though they have no
	// effect on routing, they shouldn't leak upstream and pollute
	// provider-side logs with proxy-internal diagnostics.
	upstreamReq := req
	if hasBypassMarkers(req) {
		upstreamReq = stripBypassMarkers(req)
	}
	resp, err := t.inner.RoundTrip(upstreamReq)
	class := ClassifyResponse(t.provider, resp, err)

	switch class {
	case FailureClassDegraded:
		// Record the failure so cross-instance counters and /health stats
		// still reflect reality in shadow mode.  The Store is guaranteed
		// to fail-open on Redis errors (returns StateClosed, no error),
		// so this cannot cascade.
		newState, openedNow, recErr := t.store.RecordTerminalFailure(ctx, key)
		if recErr != nil {
			t.log.Error("circuit: log-mode RecordTerminalFailure error",
				"key", key, "error", recErr)
		}
		t.maybeRecordRollup(ctx, key, openedNow)
		fc := t.enrichedFailureContext(req, resp, err, "")
		t.log.Info("circuit: log-mode terminal_failure_observed (no synthetic response, passing through)",
			append(
				fc.attrs(),
				"would_be_new_state", newState.String(),
				"mode", ModeLog,
			)...)
		t.emit("terminal_failure", fc)

	case FailureClassGlobalRateLimit:
		fc := t.enrichedFailureContext(req, resp, err, "")
		t.log.Info("circuit: log-mode global_rate_limit_observed (passing through)", fc.attrs()...)
		t.emit("global_rate_limit", fc)

	case FailureClassLocalRateLimit:
		fc := t.enrichedFailureContext(req, resp, err, "")
		t.log.Info("circuit: log-mode local_rate_limit_observed (passing through)", fc.attrs()...)
		t.emit("local_rate_limit", fc)
	}

	return resp, err
}

// maybeRecordRollup writes a key-open event to the per-provider rollup
// window iff THIS RecordTerminalFailure call is the one that flipped
// the per-key breaker Closed → Open AND the rollup feature is enabled.
// Centralised so every call site stays consistent.
func (t *Transport) maybeRecordRollup(ctx context.Context, key string, openedNow bool) {
	if !openedNow || t.cfg.PerProviderRollupThreshold <= 0 {
		return
	}
	rec, ok := t.store.(RollupRecorder)
	if !ok {
		return
	}
	if err := rec.RecordKeyOpenedForRollup(ctx, t.provider, key,
		t.cfg.PerProviderRollupWindowSeconds); err != nil {
		t.log.Warn("circuit: RecordKeyOpenedForRollup error (rollup may lag)",
			"provider", t.provider, "key", key, "error", err)
	}
}

// reArmRollup refreshes the rollup window timestamp for `key` even when
// the breaker did not just transition Closed → Open.  Called on probe
// failure so a long-running outage (same N keys continuously down for
// hours) keeps tripping the rollup signal instead of silently aging
// out.  Idempotent on (provider, key) within a window thanks to the
// dedup-by-key behaviour of RecordKeyOpenedForRollup.
func (t *Transport) reArmRollup(ctx context.Context, key string) {
	if t.cfg.PerProviderRollupThreshold <= 0 {
		return
	}
	rec, ok := t.store.(RollupRecorder)
	if !ok {
		return
	}
	if err := rec.RecordKeyOpenedForRollup(ctx, t.provider, key,
		t.cfg.PerProviderRollupWindowSeconds); err != nil {
		t.log.Warn("circuit: RecordKeyOpenedForRollup (re-arm) error",
			"provider", t.provider, "key", key, "error", err)
	}
}

// runBypass executes the request once with bypass semantics:
//
//   - never consults the per-key state, the rollup signal, or the probe slot;
//   - never returns a synthetic 503;
//   - still records an observed terminal failure (so the breaker keeps
//     learning the upstream's true health from bypass traffic);
//   - never closes an Open breaker on success (only a real half-open
//     probe can — bypass requests are not "this provider has recovered"
//     evidence).
//
// The bypass header / query param is stripped before the upstream call
// so providers do not see proxy-internal diagnostics.
func (t *Transport) runBypass(req *http.Request, reason string) (*http.Response, error) {
	ctx := req.Context()

	// Buffer the body so we can extract the model.  Without this,
	// server-side requests (which have GetBody == nil) would always
	// fall back to the bare-provider key, meaning bypass failures would
	// never feed the correct per-model breaker.
	if err := t.cacheBody(req); err != nil {
		if !errors.Is(err, errRetryBodyTooLarge) {
			return nil, fmt.Errorf("circuit: cacheBody (bypass): %w", err)
		}
		t.log.Warn(
			"circuit: bypass request body exceeds MaxRetryableBodyBytes, model attribution unavailable",
			"provider", t.provider,
			"path", req.URL.Path,
			"content_length", req.ContentLength,
			"max_retryable_body_bytes", t.cfg.MaxRetryableBodyBytes,
			"failure_kind", string(KindBodyTooLarge),
		)
	}

	key := t.keyFor(req)
	upstreamReq := stripBypassMarkers(req)

	if t.activity != nil {
		t.activity.RecordCheck()
	}

	// Tag the metric with the normalised reason so operators can audit
	// how the bypass safety valve is being used WITHOUT the
	// dogstatsd tag-cardinality blowing up.  See
	// Transport.normalizeBypassReason for the exact rules; in short:
	// safe-character filter + length cap + optional allowlist → "other".
	reasonTag := t.normalizeBypassReason(reason)

	rtStart := time.Now()
	resp, err := t.inner.RoundTrip(upstreamReq)
	upstreamDur := time.Since(rtStart)

	class := ClassifyResponse(t.provider, resp, err)
	fc := t.enrichedFailureContext(req, resp, err, "")
	tags := append(fc.metricTags(), "reason:"+reasonTag, "outcome:"+string(class))
	if t.metrics != nil {
		_ = t.metrics.Incr("circuit.bypass", tags, 1.0)
	}
	t.log.Info("circuit: bypass requested by caller",
		append(
			fc.attrs(),
			"reason", reasonTag,
			"outcome", string(class),
			"upstream_ms", upstreamDur.Milliseconds(),
		)...)

	if class == FailureClassDegraded {
		// Bypass requests still feed observability so operators can see
		// real upstream health even when callers route around the
		// breaker.  This may itself cause the per-key breaker to open;
		// that is fine — the next non-bypass request will fast-fail and
		// the next bypass request will keep going through.  The two
		// modes are intentionally orthogonal.
		_, openedNow, recErr := t.store.RecordTerminalFailure(ctx, key)
		if recErr != nil {
			t.log.Error("circuit: bypass RecordTerminalFailure error",
				"key", key, "error", recErr)
		}
		t.maybeRecordRollup(ctx, key, openedNow)
	}

	return resp, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Core retry loops
// ─────────────────────────────────────────────────────────────────────────────

// slowRequestThreshold is the floor above which the success path emits an
// info-level breakdown of where time was spent (cacheBody vs upstream
// RoundTrip).  Sub-threshold requests stay silent so happy traffic does not
// drown the logs; anything slower than this gets a single per-step
// breakdown line so latency regressions are immediately attributable.
const slowRequestThreshold = 5 * time.Second

// runWithRetries executes the request with the configured retry policies and
// records terminal failures in the circuit store.
func (t *Transport) runWithRetries(req *http.Request) (*http.Response, error) {
	if hasBypassMarkers(req) {
		req = stripBypassMarkers(req)
	}

	ctx := req.Context()
	startedAt := time.Now()

	// Ensure the body can be re-read on retries.  When the body is too
	// large to buffer in memory we gracefully disable retries and fall
	// through to a single-pass RoundTrip so oversize requests (e.g. a
	// multi-megabyte vision payload) still reach the upstream without
	// giving a client an unbounded-memory DoS vector.
	cacheBodyStart := time.Now()
	if err := t.cacheBody(req); err != nil {
		if errors.Is(err, errRetryBodyTooLarge) {
			t.log.Warn(
				"circuit: request body exceeds MaxRetryableBodyBytes, retries disabled for this request",
				"provider", t.provider,
				"path", req.URL.Path,
				"content_length", req.ContentLength,
				"max_retryable_body_bytes", t.cfg.MaxRetryableBodyBytes,
				"failure_kind", string(KindBodyTooLarge),
			)
			return t.inner.RoundTrip(req)
		}
		return nil, fmt.Errorf("circuit: cacheBody: %w", err)
	}
	cacheBodyDur := time.Since(cacheBodyStart)

	// Re-check breaker state under the now-correct per-model key.  The
	// initial check in RoundTrip had to defer checking the bare-provider
	// key because req.GetBody was nil for incoming server-side requests
	// until cacheBody ran, and we didn't want to prematurely fast-fail
	// a request that might actually belong to a healthy per-model key.
	// Now that cacheBody has run, effectiveStateForRequest can safely
	// join the per-key state with provider rollup without starving
	// half-open probes.
	state, key := t.effectiveStateForRequest(req)
	if t.activity != nil {
		t.activity.RecordCheck()
	}
	switch state {
	case StateOpen:
		fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Warn("circuit: fast-fail per-model breaker open",
			append(fc.attrs(), "mode", ModeEnforce, "stage", "post_cache_body")...)
		t.emit("fast_fail", fc)
		if t.activity != nil {
			t.activity.RecordFastFail(t.provider, key)
		}
		return t.degradedResponse(req), nil
	case StateHalfOpen:
		return t.runProbe(req, key)
	}

	var (
		transientAttempts int
		rateLimitAttempts int
		// upstreamDur is the time spent inside the inner RoundTripper
		// (TLS handshake + body upload + upstream processing + first
		// response byte).  Accumulates across retries so the breakdown
		// log line below reflects the full wall-clock cost.
		upstreamDur time.Duration
	)

	// Track whether we've had any rate-limit failures for escalation logic.
	var firstRateLimitAt time.Time

	// Remember the most recent failure context so log lines / metrics
	// emitted after the response body is drained still carry the
	// upstream status code, kind, and error string.  StatusCode is the
	// only field we read off of resp post-drain so this is safe.
	var (
		lastResp *http.Response
		lastErr  error
	)

	for {
		// Bail out early if the caller has gone away.  Without this check
		// we would still consume retry budget and record terminal
		// failures for requests whose downstream has already cancelled.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		attempt := transientAttempts + rateLimitAttempts
		attemptReq := req.WithContext(context.WithValue(ctx, retryAttemptKey{}, attempt))
		if attempt > 0 {
			// Restore the body for the retry.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("circuit: GetBody on retry: %w", err)
				}
				attemptReq.Body = body
			}
		}

		// ── test-mode: force_transient_recover ────────────────────────────
		// Fail on attempt 0, forward to the real inner transport on attempt 1+.
		// Gated on Config.TestModeEnabled — see the header docstring on
		// Transport.testModeFromRequest for why that gate exists.
		if t.testModeFromRequest(req) == TestModeForceTransientRecover {
			if attempt == 0 {
				t.log.Info("circuit: test-mode force_transient_recover (attempt 0 → fail)",
					"provider", t.provider)
				// Simulate a degraded failure and let the retry loop continue.
				backoff := transientBackoff(transientAttempts)
				if err := t.sleep(ctx, backoff); err != nil {
					return nil, err
				}
				transientAttempts++
				continue
			}
			// Strip the test mode header on retries so the real inner transport
			// does not see it.
			inner := attemptReq.Clone(ctx)
			inner.Header.Del(TestModeHeader)
			attemptReq = inner
		}

		rtStart := time.Now()
		resp, err := t.inner.RoundTrip(attemptReq)
		upstreamDur += time.Since(rtStart)
		fc := ClassifyResponse(t.provider, resp, err)

		// ── Success ───────────────────────────────────────────────────────
		if fc == FailureClassNone {
			t.logTimingBreakdown(req, startedAt, cacheBodyDur, upstreamDur, attempt+1, resp)
			return resp, err
		}

		// ── Insufficient quota / billing cap — open the whole provider ───────
		// OpenAI 429 insufficient_quota is an account-wide billing cap, not a
		// model-specific throttle: while it persists EVERY model for the
		// provider will fail identically until billing is topped up.  That is
		// functionally a provider outage, so a single occurrence trips the
		// provider-level rollup immediately (bypassing the N-distinct-key
		// threshold) — every subsequent OpenAI request then fast-fails with a
		// synthetic 503 so callers can fall over instead of each eating the
		// full-latency error.
		//
		// The triggering request itself still returns the REAL upstream
		// response (status + body, undrained) so this caller sees the
		// actionable insufficient_quota error code.  Retrying is pointless and
		// is therefore skipped.  The forced-open marker auto-expires after the
		// rollup window, at which point traffic is allowed through to re-probe;
		// if quota is still exhausted the next failure re-arms it.
		if fc == FailureClassInsufficientQuota {
			upstreamDetail := peekUpstreamErrorDetail(t.provider, resp)
			evt := t.enrichedFailureContext(req, resp, err, upstreamDetail)
			// Open the BARE-provider breaker so every OpenAI model fast-fails
			// until recovery.  ForceOpen uses one cooldown period, after which
			// the breaker auto-promotes to HalfOpen and a single probe re-tests
			// the upstream — if quota is still exhausted that probe re-opens it,
			// otherwise it closes.  (No retry here: retrying a billing cap is
			// pointless.)
			if fErr := t.store.ForceOpen(ctx, t.provider, t.cfg.CooldownSeconds); fErr != nil {
				t.log.Warn("circuit: ForceOpen (insufficient_quota) error",
					"provider", t.provider, "error", fErr)
			} else if t.activity != nil {
				t.activity.RecordOpened(t.provider, t.provider, "insufficient_quota", string(evt.Kind), evt.UpstreamError, evt.StatusCode)
			}
			t.log.Warn("circuit: insufficient_quota — forcing provider open, passing upstream response through",
				append(evt.attrs(),
					"cooldown_seconds", t.cfg.CooldownSeconds)...)
			t.emit("insufficient_quota_force_open", evt)
			t.logTimingBreakdown(req, startedAt, cacheBodyDur, upstreamDur, attempt+1, resp)
			return resp, err
		}

		// Capture before drain so failure logs retain the upstream error
		// body (Gemini UNAVAILABLE, OpenAI insufficient_quota, etc.).
		// lastResp.Body is not safe to read after the drain below.
		upstreamDetail := peekUpstreamErrorDetail(t.provider, resp)
		lastResp = resp
		lastErr = err

		// Drain the response body before retrying so the connection is
		// returned to the pool cleanly.
		drainResponseBody(resp)

		retryAfterSec := retryAfterSeconds(resp)

		// ── Rate-limit handling ───────────────────────────────────────────
		if fc == FailureClassLocalRateLimit || fc == FailureClassGlobalRateLimit {
			st := &retryLoopState{
				transientAttempts: transientAttempts,
				rateLimitAttempts: rateLimitAttempts,
				firstRateLimitAt:  firstRateLimitAt,
				lastResp:          lastResp,
				lastErr:           lastErr,
				lastUpstreamError: upstreamDetail,
			}
			resp2, err2, done := t.handleRateLimitFailure(ctx, req, key, fc, retryAfterSec, st)
			rateLimitAttempts = st.rateLimitAttempts
			firstRateLimitAt = st.firstRateLimitAt
			if done {
				return resp2, err2
			}
			continue
		}

		// ── Degraded / transient handling ─────────────────────────────────
		// NOTE on the bare `_ =` discards that appear in this block, in
		// runProbe, and in the bypass path: every call into t.store
		// (RecordTerminalFailure / RecordSuccess / RecordProbeFailed)
		// is intentionally best-effort.  A Redis hiccup, lock-key
		// contention, or transient network blip on the state-store
		// path must never affect request flow — the worst outcome is a
		// slightly stale circuit-breaker counter on one node, which
		// the next call will re-record.  Same convention as t.emit
		// above.  Where a return value IS used (e.g. `openedNow`) we
		// still drop the error: if err != nil, openedNow is the zero
		// value (false), so downstream branches behave safely.
		if fc == FailureClassDegraded {
			st := &retryLoopState{
				transientAttempts: transientAttempts,
				lastResp:          lastResp,
				lastErr:           lastErr,
				lastUpstreamError: upstreamDetail,
			}
			resp2, err2, done := t.handleDegradedFailure(ctx, req, key, resp, err, st)
			transientAttempts = st.transientAttempts
			if done {
				return resp2, err2
			}
			continue
		}

		// ── Unknown / unclassifiable failure — pass through as-is ─────────
		return resp, err
	}
}

// retryLoopState is the mutable per-iteration state runWithRetries
// hands to the per-failure-class handlers below.  It exists so the
// handlers can update attempt counters and remember the most recent
// upstream attempt without forcing runWithRetries to pass eight
// separate pointer arguments.  Fields are mutated in place by the
// handlers; the caller copies the updated counters back into its own
// loop-scoped variables after the call returns.
type retryLoopState struct {
	transientAttempts int
	rateLimitAttempts int
	firstRateLimitAt  time.Time
	lastResp          *http.Response // post-drain — only StatusCode / Header safe
	lastErr           error
	lastUpstreamError string // parsed from body before drain
}

// handleRateLimitFailure runs the rate-limit branch of runWithRetries.
//
// The return tuple is (resp, err, done): when done is true the caller
// returns (resp, err) immediately; when done is false the caller
// continues the retry loop after st has been updated with the new
// attempt count + firstRateLimitAt timestamp.
//
// Behaviour preserved verbatim from the original inline block:
//  1. retries exhausted + sustained global rate limit → escalate to
//     handleTerminalFailure (provider_degraded).
//  2. retries exhausted otherwise → synthetic rate-limit response.
//  3. not exhausted → log + sleep + increment + continue.
func (t *Transport) handleRateLimitFailure(
	ctx context.Context,
	req *http.Request,
	key string,
	fc FailureClass,
	retryAfterSec int,
	st *retryLoopState,
) (*http.Response, error, bool) {
	if st.rateLimitAttempts >= t.cfg.MaxRateLimitRetries {
		evt := t.enrichedFailureContext(req, st.lastResp, st.lastErr, st.lastUpstreamError)
		t.log.Warn("circuit: rate-limit retries exhausted",
			append(
				evt.attrs(),
				"attempts", st.rateLimitAttempts,
				"class", fc,
			)...)
		t.emit("rate_limit_exhausted", evt)

		// Escalate sustained global rate limits to degraded.
		if fc == FailureClassGlobalRateLimit && !st.firstRateLimitAt.IsZero() {
			elapsed := time.Since(st.firstRateLimitAt).Seconds()
			if int(elapsed) >= t.cfg.GlobalRateLimitEscalationWindow {
				t.log.Warn("circuit: global rate-limit escalated to provider_degraded",
					append(evt.attrs(), "elapsed_seconds", elapsed)...)
				resp, err := t.handleTerminalFailure(ctx, req, key, st.lastResp, st.lastErr, st.lastUpstreamError)
				return resp, err, true
			}
		}
		// Synthetic rate-limit error (no magic string — not degraded,
		// just throttled).
		return t.rateLimitResponse(fc), nil, true
	}
	if fc == FailureClassGlobalRateLimit && st.firstRateLimitAt.IsZero() {
		st.firstRateLimitAt = time.Now()
	}
	backoff := rateLimitBackoff(retryAfterSec, st.rateLimitAttempts)
	t.log.Info(
		"circuit: rate-limit backoff",
		"provider", t.provider,
		"class", fc,
		"backoff_ms", backoff.Milliseconds(),
		"attempt", st.rateLimitAttempts+1,
	)
	if err := t.sleep(ctx, backoff); err != nil {
		return nil, err, true
	}
	st.rateLimitAttempts++
	return nil, nil, false
}

// handleDegradedFailure runs the transient/degraded branch of
// runWithRetries.  Same (resp, err, done) contract as
// handleRateLimitFailure above.
//
// Behaviour preserved verbatim from the original inline block:
//  1. Per RetryContributionMode, optionally count this retried failure
//     toward the degradation score (or log the counterfactual).
//  2. retries exhausted → handleTerminalFailure.
//  3. not exhausted → log + sleep + increment + continue.
func (t *Transport) handleDegradedFailure(
	ctx context.Context,
	req *http.Request,
	key string,
	resp *http.Response,
	err error,
	st *retryLoopState,
) (*http.Response, error, bool) {
	switch t.cfg.RetryContributionMode {
	case "on":
		t.log.Info("circuit: retried failure contributing to degradation score",
			append(t.enrichedFailureContext(req, resp, err, st.lastUpstreamError).attrs(),
				"attempt", st.transientAttempts)...)
		_, openedNow, _ := t.store.RecordTerminalFailure(ctx, key)
		t.maybeRecordRollup(ctx, key, openedNow)
	case "log":
		t.log.Info("circuit: retried failure would_have_contributed_to_degradation",
			append(t.enrichedFailureContext(req, resp, err, st.lastUpstreamError).attrs(),
				"attempt", st.transientAttempts)...)
	}

	if st.transientAttempts >= t.cfg.MaxTransientRetries {
		t.log.Warn("circuit: transient retries exhausted, recording terminal failure",
			append(t.enrichedFailureContext(req, st.lastResp, st.lastErr, st.lastUpstreamError).attrs(),
				"attempts", st.transientAttempts)...)
		respT, errT := t.handleTerminalFailure(ctx, req, key, st.lastResp, st.lastErr, st.lastUpstreamError)
		return respT, errT, true
	}

	backoff := transientBackoff(st.transientAttempts)
	t.log.Info(
		"circuit: transient backoff",
		"provider", t.provider,
		"backoff_ms", backoff.Milliseconds(),
		"attempt", st.transientAttempts+1,
	)
	if err := t.sleep(ctx, backoff); err != nil {
		return nil, err, true
	}
	st.transientAttempts++
	return nil, nil, false
}

// logTimingBreakdown emits a single per-request line showing where wall-clock
// time was spent on the happy path (cacheBody buffering vs. inner RoundTrip).
// We only log when the request crosses slowRequestThreshold so successful sub-
// second traffic stays quiet, but anything slow gets per-step attribution
// inline — this is what answers "did the circuit breaker add latency?"
// without needing to attach a profiler.
func (t *Transport) logTimingBreakdown(
	req *http.Request,
	startedAt time.Time,
	cacheBody, upstream time.Duration,
	attempts int,
	resp *http.Response,
) {
	total := time.Since(startedAt)
	if total < slowRequestThreshold {
		return
	}
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	path := ""
	method := ""
	if req != nil {
		method = req.Method
		if req.URL != nil {
			path = req.URL.Path
		}
	}
	// overhead = total - upstream - cacheBody; should be ~0.  A positive
	// value means the circuit breaker bookkeeping (Store calls, retry
	// backoff sleeps, etc.) is contributing.
	overhead := total - upstream - cacheBody
	t.log.Info(
		"circuit: slow request timing breakdown",
		"provider", t.provider,
		"path", path,
		"method", method,
		"status_code", statusCode,
		"attempts", attempts,
		"total_ms", total.Milliseconds(),
		"upstream_ms", upstream.Milliseconds(),
		"cache_body_ms", cacheBody.Milliseconds(),
		"overhead_ms", overhead.Milliseconds(),
	)
}

// runProbe executes a single half-open probe request (no retries).
//
// key is the per-request breaker key (provider:model or provider) — passed
// in by the caller so the probe-slot acquisition, RecordSuccess, and
// RecordProbeFailed all target the SAME key that GetState observed as
// HalfOpen.  Recomputing it inside runProbe would risk drifting if the
// extractor reads a different field from the cached body.
func (t *Transport) runProbe(req *http.Request, key string) (*http.Response, error) {
	if hasBypassMarkers(req) {
		req = stripBypassMarkers(req)
	}

	ctx := req.Context()

	// Buffer the body up to MaxRetryableBodyBytes so the model extractor
	// has something replayable to read on the failure path, and so the
	// upstream RoundTrip is never handed a body that the extractor has
	// already drained.  Probes don't retry, but cacheBody is what makes
	// req.GetBody non-nil — without it the modelFn guard in
	// newFailureContext correctly skips extraction and probe_failed
	// metrics lose model attribution.  Oversize bodies fall through to
	// a streaming pass-through with model:"unknown" rather than failing
	// the probe.
	if err := t.cacheBody(req); err != nil && !errors.Is(err, errRetryBodyTooLarge) {
		return nil, fmt.Errorf("circuit: cacheBody (probe): %w", err)
	}

	acquired, stopLease := t.acquireProbeSlot(ctx, key)
	if !acquired {
		// Another probe is already in flight — fast-fail this request.
		evt := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Info("circuit: half-open probe already in flight, fast-failing", evt.attrs()...)
		t.emit("fast_fail", evt)
		if t.activity != nil {
			t.activity.RecordFastFail(t.provider, key)
		}
		return t.degradedResponse(req), nil
	}
	if stopLease != nil {
		defer stopLease()
	}

	if t.activity != nil {
		t.activity.RecordProbe(t.provider, key)
	}

	resp, err := t.inner.RoundTrip(req)

	// If the caller's context was cancelled or its deadline expired,
	// we learned nothing about the provider's actual health.  Silently
	// releasing the probe slot (without flipping state in either
	// direction) is the correct outcome: a subsequent request will try
	// the probe again, instead of either (a) prematurely closing the
	// circuit because we saw FailureClassNone, or (b) re-opening for a
	// full cooldown because we saw "some error".  We check ctx.Err()
	// directly rather than errors.Is(err, context.Canceled) because
	// http.RoundTrip wraps context errors inside *url.Error, and some
	// transports surface the cancellation as an io.EOF when the body
	// is drained mid-flight.
	if ctxErr := ctx.Err(); ctxErr != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		t.log.Info("circuit: probe aborted by caller context, releasing probe slot without state change",
			append(
				t.enrichedFailureContext(req, resp, err, "").attrs(),
				"ctx_err", truncateError(ctxErr),
			)...)
		drainResponseBody(resp)
		t.releaseProbeSlotDetached(key)
		return nil, err
	}

	fc := ClassifyResponse(t.provider, resp, err)

	if fc == FailureClassNone {
		return t.recordProbeSuccess(ctx, req, resp, err, key)
	}
	return t.recordProbeFailure(ctx, req, resp, err, key)
}

// acquireProbeSlot is the half-open coordination dance: take the
// (single-flight) probe slot, then if the store supports background
// lease refresh, start it and return a stop function the caller defers.
// Returns acquired=false when another probe holds the slot.
func (t *Transport) acquireProbeSlot(ctx context.Context, key string) (acquired bool, stopLease func()) {
	type probeStarter interface {
		TryStartProbe(ctx context.Context, key string) bool
	}
	if ps, ok := t.store.(probeStarter); ok {
		if !ps.TryStartProbe(ctx, key) {
			return false, nil
		}
	}

	// For distributed stores the probe slot is held by a TTL-bounded
	// Redis key; if the upstream round-trip exceeds that TTL a parallel
	// probe could win the slot.  KeepProbeAlive starts a background
	// lease-refresher that extends the TTL until we're done.  MemoryStore
	// does not expose this and is a no-op here (stopLease == nil).
	type probeLeaser interface {
		KeepProbeAlive(key string) func()
	}
	if pl, ok := t.store.(probeLeaser); ok {
		return true, pl.KeepProbeAlive(key)
	}
	return true, nil
}

// releaseProbeSlotDetached releases the half-open probe slot using a
// fresh short-lived context.  Called from the caller-aborted branch in
// runProbe where the request context is already dead, so the release
// itself must not piggyback on it.  Errors are intentionally swallowed:
// the slot is TTL-bounded server-side and will fall off eventually.
func (t *Transport) releaseProbeSlotDetached(key string) {
	type probeReleaser interface {
		ReleaseProbe(ctx context.Context, key string) error
	}
	pr, ok := t.store.(probeReleaser)
	if !ok {
		return
	}
	relCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = pr.ReleaseProbe(relCtx, key)
}

// recordProbeSuccess closes the per-key breaker after a healthy probe
// response and drops the key out of the per-provider rollup window so
// the rollup signal tracks "currently degraded" rather than "tripped
// at any point in the last window".
func (t *Transport) recordProbeSuccess(ctx context.Context, req *http.Request, resp *http.Response, err error, key string) (*http.Response, error) {
	_ = req // currently unused; keep for parity with recordProbeFailure
	probeStatus := 0
	if resp != nil {
		probeStatus = resp.StatusCode
	}
	t.log.Info(
		"circuit: probe succeeded, closing circuit",
		"provider", t.provider,
		"cb_key", key,
		"status_code", probeStatus,
		"new_state", StateClosed.String(),
	)
	if t.activity != nil {
		t.activity.RecordProbeClosed(t.provider, key, probeStatus)
	}
	_ = t.store.RecordSuccess(ctx, key)
	if t.cfg.PerProviderRollupThreshold > 0 {
		if rec, ok := t.store.(RollupRecorder); ok {
			_ = rec.ClearRollupKey(ctx, t.provider, key)
		}
	}
	return resp, err
}

// recordProbeFailure re-opens the per-key breaker after a failed probe
// and refreshes the key's rollup-window timestamp so a long-running
// outage stays tripped instead of aging out after the original
// Closed → Open event expires.
func (t *Transport) recordProbeFailure(ctx context.Context, req *http.Request, resp *http.Response, err error, key string) (*http.Response, error) {
	upstreamDetail := ""
	if resp != nil {
		upstreamDetail = peekUpstreamErrorDetail(t.provider, resp)
	}
	evt := t.enrichedFailureContext(req, resp, err, upstreamDetail)
	t.log.Warn("circuit: probe failed, re-opening circuit",
		append(evt.attrs(), "new_state", StateOpen.String())...)
	t.emit("probe_failed", evt)
	if t.activity != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.activity.RecordProbeReopened(t.provider, key, status, string(evt.Kind), evt.UpstreamError)
	}
	drainResponseBody(resp)
	_ = t.store.RecordProbeFailed(ctx, key)
	t.reArmRollup(ctx, key)
	return t.degradedResponse(req), nil
}

// handleTerminalFailure records the failure, potentially opens the circuit,
// and returns the appropriate synthesised response to the caller.
//
// lastResp / lastErr describe the most recent upstream attempt (post-
// drain — only StatusCode / Header are safe to read on lastResp) so the
// emitted log line and dogstatsd counter retain status_code, model,
// failure_kind, and error context that would otherwise be lost between
// retry exhaustion and synthetic-response emission.  upstreamError was
// parsed from the response body before drain.
func (t *Transport) handleTerminalFailure(ctx context.Context, req *http.Request, key string, lastResp *http.Response, lastErr error, upstreamError string) (*http.Response, error) {
	evt := t.enrichedFailureContext(req, lastResp, lastErr, upstreamError)
	newState, openedNow, err := t.store.RecordTerminalFailure(ctx, key)
	if err != nil {
		t.log.Error("circuit: RecordTerminalFailure error", "key", key, "error", err)
	}
	if openedNow && t.activity != nil {
		t.activity.RecordOpened(t.provider, key, "threshold", string(evt.Kind), evt.UpstreamError, evt.StatusCode)
	}
	t.maybeRecordRollup(ctx, key, openedNow)

	attrs := append(
		evt.attrs(),
		"new_state", newState.String(),
		"mode", ModeEnforce,
	)

	if newState == StateOpen {
		t.log.Warn("circuit: threshold crossed — circuit opened", attrs...)
	}

	t.log.Warn("circuit: terminal failure, returning degraded signal", attrs...)
	t.emit("terminal_failure", evt)
	return t.degradedResponse(req), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Synthetic response builders
// ─────────────────────────────────────────────────────────────────────────────

// degradedResponse returns a synthetic HTTP 503 response whose JSON body
// contains Config.DegradedSignal so downstream clients can detect
// proxy-originated provider degradation (see DefaultDegradedSignal).
func (t *Transport) degradedResponse(req *http.Request) *http.Response {
	body := buildDegradedBody(t.provider, t.cfg.DegradedSignal)
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":            []string{"application/json"},
			"X-Llm-Proxy-Error-Class": []string{string(FailureClassDegraded)},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// rateLimitResponse returns a synthetic 429 without the DegradedSignal — the
// request is throttled but the provider is not considered degraded.
func (t *Transport) rateLimitResponse(fc FailureClass) *http.Response {
	body := []byte(`{"error":{"message":"Rate limit exceeded; please retry later.","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":            []string{"application/json"},
			"X-Llm-Proxy-Error-Class": []string{string(fc)},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// buildDegradedBody returns a JSON error body containing the configured
// degraded signal.  An empty signal falls back to DefaultDegradedSignal so
// the body is never unmarked.
func buildDegradedBody(provider, signal string) []byte {
	if signal == "" {
		signal = DefaultDegradedSignal
	}
	msg := fmt.Sprintf("%s Provider %s is currently degraded or unavailable. Please try again later.",
		signal, provider)
	body := map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "provider_degraded",
			"code":    "provider_degraded",
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Backoff helpers
// ─────────────────────────────────────────────────────────────────────────────

// transientBackoff returns a jittered backoff duration for transient failures.
// attempt is 0-indexed (0 = first retry).
func transientBackoff(attempt int) time.Duration {
	base := 500 * time.Millisecond
	exp := base * (1 << uint(attempt)) // 500ms, 1s, 2s, ...
	jitter := time.Duration(rand.Int63n(int64(exp / 2)))
	return exp + jitter
}

// rateLimitBackoff returns a backoff duration for rate-limit failures,
// honouring a provider-supplied Retry-After value when available.
func rateLimitBackoff(retryAfterSec, attempt int) time.Duration {
	if retryAfterSec > 0 {
		return time.Duration(retryAfterSec) * time.Second
	}
	base := 1 * time.Second
	exp := base * (1 << uint(attempt)) // 1s, 2s, 4s, ...
	jitter := time.Duration(rand.Int63n(int64(exp / 2)))
	return exp + jitter
}

// retryAfterSeconds extracts the Retry-After header value as seconds.
func retryAfterSeconds(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return parseRetryAfterSeconds(resp.Header.Get("Retry-After"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Misc helpers
// ─────────────────────────────────────────────────────────────────────────────

// cacheBody reads req.Body into memory and replaces it with a NopCloser
// backed by the bytes, setting GetBody so retries can re-read it.
// A no-op if req.Body is nil or already has GetBody.
//
// Returns errRetryBodyTooLarge (a sentinel the caller is expected to
// handle) when the body exceeds Config.MaxRetryableBodyBytes, rather
// than either (a) allocating the full payload anyway — which gives a
// client an easy memory-DoS primitive — or (b) returning a hard error
// that fails otherwise-legitimate large requests.  On overflow we
// leave req.Body as a streaming reader that still carries every byte
// we observed (via MultiReader over the peeked prefix + the rest of
// the original body), so the caller can still forward the request on
// a best-effort, no-retry basis.
func (t *Transport) cacheBody(req *http.Request) error {
	if req.Body == nil || req.GetBody != nil {
		return nil
	}
	cap := t.cfg.MaxRetryableBodyBytes
	if cap <= 0 {
		cap = DefaultMaxRetryableBodyBytes
	}

	// Content-Length short-circuit: if the client explicitly tells us
	// the body is too big we skip the read entirely and leave the body
	// intact for a streaming pass-through.
	if req.ContentLength > cap {
		return errRetryBodyTooLarge
	}

	// Read at most cap+1 bytes so we can detect overflow even when
	// Content-Length is unknown / chunked.  io.LimitReader is safe
	// against a body that lies about its length.
	limited := &io.LimitedReader{R: req.Body, N: cap + 1}
	prefix, err := io.ReadAll(limited)
	if err != nil {
		req.Body.Close() //nolint:errcheck
		return err
	}

	if int64(len(prefix)) > cap {
		// Overflow: rewind what we read into the front of the body so a
		// downstream streaming pass-through sees the full payload, and
		// let the caller know retries are not possible for this req.
		req.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.MultiReader(bytes.NewReader(prefix), req.Body),
			Closer: req.Body,
		}
		return errRetryBodyTooLarge
	}

	// Fits in memory — buffer it and enable retries.
	if err := req.Body.Close(); err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(prefix))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(prefix)), nil
	}
	req.ContentLength = int64(len(prefix))
	return nil
}

// sleep blocks for d, respecting context cancellation.
func (t *Transport) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// testModeFromRequest returns the test mode value from the header or, as a
// fallback, the URL query parameter.  When Config.TestModeEnabled is
// false it unconditionally returns "" so a client cannot smuggle
// X-LLM-Proxy-Test-Mode or llm_proxy_test_mode past a production
// deployment to force synthetic degraded responses.
//
// This is a method on Transport (rather than a free function) precisely
// so we always route through the config gate.  Callers inside this
// package MUST NOT read those fields directly.
func (t *Transport) testModeFromRequest(req *http.Request) string {
	if !t.cfg.TestModeEnabled {
		return ""
	}
	if v := req.Header.Get(TestModeHeader); v != "" {
		return v
	}
	if req.URL != nil {
		return req.URL.Query().Get(TestModeQueryParam)
	}
	return ""
}

// ProviderFromPath derives the provider name from the URL path prefix
// (e.g. "/openai/v1/chat/completions" → "openai").
//
// The Bedrock SigV4 passthrough mounts the AWS-canonical "/model/..."
// path in addition to "/bedrock/..." because boto3 signs requests
// against the canonical hostname; without this rewrite the circuit
// breaker observability and test-mode allowlist would see "model" as
// a phantom provider when an SDK uses the passthrough.
//
// Compatibility routes for Gemini ("/v1/models/gemini..." and
// "/v1beta/models/gemini...") return "gemini" — matching how
// ProviderManager.ProviderForRequest resolves them.
func ProviderFromPath(path string) string {
	if strings.HasPrefix(path, "/model/") || path == "/model" {
		return "bedrock"
	}
	if strings.HasPrefix(path, "/v1/models/gemini") || strings.HasPrefix(path, "/v1beta/models/gemini") {
		return "gemini"
	}
	path = strings.TrimPrefix(path, "/")
	if idx := strings.Index(path, "/"); idx > 0 {
		return path[:idx]
	}
	return path
}
