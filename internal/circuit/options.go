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
