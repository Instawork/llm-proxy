package circuit

import (
	"net/http"
)

// MetricsSink is the minimal dogstatsd-shaped surface the circuit transport
// uses to emit counters when failure / state events are observed.
//
// The signature deliberately matches *github.com/DataDog/datadog-go/v5/statsd.Client.Incr
// so callers can pass a real statsd client directly without writing an
// adapter.  Nil-safe: NewTransport installs a no-op sink if none is
// supplied via WithMetrics, so transports built in tests or in deployments
// without a Datadog Agent stay completely silent.
type MetricsSink interface {
	Incr(name string, tags []string, rate float64) error
}

// noopMetrics drops every event; used when the caller has not wired a
// real sink so call sites can emit metrics unconditionally.
type noopMetrics struct{}

func (noopMetrics) Incr(string, []string, float64) error { return nil }

// ModelFromRequestFunc returns the model name parsed out of an HTTP
// request, or "" if the model cannot be determined.
//
// Callers should treat this as a best-effort, side-effect-free hint: it
// is only used to enrich log lines and metric tags when an upstream
// failure is observed, never to influence routing or retry behaviour.
//
// Implementations are expected to:
//
//   - tolerate nil bodies, missing GetBody, and oversize requests;
//   - not block or perform I/O beyond an in-memory buffer read;
//   - be safe to call concurrently on the same request (the circuit
//     transport may invoke them inside log helpers from probe or
//     retry paths).
type ModelFromRequestFunc func(*http.Request) string

// CallerFromRequestFunc returns a low-cardinality, human-readable label
// for the caller behind an HTTP request (e.g. the proxy API-key
// description like "finch-prod"), or "" if it cannot be determined.
//
// It is used only to enrich failure log lines and metric tags so
// operators can attribute degraded responses to a downstream caller —
// never to influence routing or breaker behaviour. Like
// ModelFromRequestFunc, implementations must be side-effect-free,
// non-blocking, and safe for concurrent use; in practice the caller
// label lives on the request context, so no body read is required.
//
// IMPORTANT: return a stable, bounded label (a key *name*, not the
// secret key value and not a per-request id) — the value becomes a
// Datadog tag and unbounded values blow the cardinality budget.
type CallerFromRequestFunc func(*http.Request) string

// Option mutates a Transport during construction.  See NewTransport for
// the canonical usage.
type Option func(*Transport)

// WithMetrics installs a dogstatsd-style sink so the circuit transport
// can emit counters (e.g. circuit.terminal_failure, circuit.fast_fail)
// alongside its log lines.  Pass a nil sink to keep the no-op default.
func WithMetrics(m MetricsSink) Option {
	return func(t *Transport) {
		if m == nil {
			return
		}
		t.metrics = m
	}
}

// WithModelExtractor installs a provider-aware model-name extractor.
// The transport calls it on failure paths so successful traffic
// pays no extra cost.  Pass nil (or omit the option entirely) to skip
// model tagging.
func WithModelExtractor(f ModelFromRequestFunc) Option {
	return func(t *Transport) {
		t.modelFn = f
	}
}

// WithCallerExtractor installs a caller-label extractor so failure log
// lines and metric tags carry a `caller` dimension (e.g. the proxy
// API-key description). The transport calls it only on failure paths, so
// successful traffic pays no extra cost. Pass nil (or omit the option) to
// skip caller tagging — the tag then reports "unknown".
func WithCallerExtractor(f CallerFromRequestFunc) Option {
	return func(t *Transport) {
		t.callerFn = f
	}
}

// ActivityRecorder is optional dashboard telemetry for circuit state transitions.
// Implementations must be safe for concurrent use.
type ActivityRecorder interface {
	RecordCheck()
	RecordFastFail(provider, key string)
	RecordProbe(provider, key string)
	RecordProbeClosed(provider, key string, statusCode int)
	RecordProbeReopened(provider, key string, statusCode int, failureKind, upstreamError string)
	RecordOpened(provider, key, reason, failureKind, upstreamError string, statusCode int)
}

// WithActivityRecorder wires dashboard activity counters/events. Pass nil to
// keep the default (no recording).
func WithActivityRecorder(r ActivityRecorder) Option {
	return func(t *Transport) {
		t.activity = r
	}
}
