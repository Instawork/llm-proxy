package observability

import (
	"fmt"
	"log/slog"

	"github.com/DataDog/datadog-go/v5/statsd"

	configPkg "github.com/Instawork/llm-proxy/internal/config"
)

// MetricsSink is the minimal dogstatsd surface feature packages use for
// observability emission.
type MetricsSink interface {
	Incr(name string, tags []string, rate float64) error
	Distribution(name string, value float64, tags []string, rate float64) error
}

type noopMetrics struct{}

func (noopMetrics) Incr(string, []string, float64) error                  { return nil }
func (noopMetrics) Distribution(string, float64, []string, float64) error { return nil }

// NoopSink is a metrics sink that drops all events.
var NoopSink MetricsSink = noopMetrics{}

// NewMetricsSink builds a dogstatsd client from a feature-local Datadog
// config block. Returns a no-op sink when cfg is nil.
func NewMetricsSink(cfg *configPkg.DatadogTransportConfig, logger *slog.Logger, feature string) MetricsSink {
	if cfg == nil {
		return noopMetrics{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == "" {
		port = "8125"
	}
	namespace := cfg.Namespace
	if namespace == "" {
		namespace = "llm"
	}
	addr := fmt.Sprintf("%s:%s", host, port)
	client, err := statsd.New(
		addr,
		statsd.WithNamespace(namespace),
		statsd.WithTags(cfg.Tags),
	)
	if err != nil {
		logger.Warn(
			"dogstatsd metrics disabled: client construction failed",
			"feature", feature,
			"error", err,
			"addr", addr,
		)
		return noopMetrics{}
	}
	logger.Info(
		"dogstatsd metrics enabled",
		"feature", feature,
		"addr", addr,
		"namespace", namespace,
		"tags", cfg.Tags,
	)
	return client
}
