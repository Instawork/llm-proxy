package middleware

import (
	"net/http"
	"strconv"

	"github.com/Instawork/llm-proxy/internal/observability"
)

// UnparsedMetricName is the counter emitted for every UnparsedResponse. With
// the usual "llm" namespace it is llm.requests.unparsed.
const UnparsedMetricName = "requests.unparsed"

// maxUnparsedModelTagLen bounds the client-supplied model name on the
// counter, as circuit.normalizeTagValue does for the circuit metrics.
const maxUnparsedModelTagLen = 200

// NewUnparsedMetricsCallback returns an UnparsedCallback that counts
// responses the provider billed but the cost tracker never saw. Those are
// absent from every llm.tokens.* and llm.cost.* metric, so without this
// counter a spend monitor reads zero for them.
func NewUnparsedMetricsCallback(sink observability.MetricsSink) UnparsedCallback {
	if sink == nil {
		sink = observability.NoopSink
	}
	return func(_ *http.Request, info UnparsedResponse) {
		model := info.Model
		if model == "" {
			model = "unknown"
		}
		if len(model) > maxUnparsedModelTagLen {
			model = model[:maxUnparsedModelTagLen]
		}
		// A dropped UDP packet must never affect the request.
		_ = sink.Incr(UnparsedMetricName, []string{
			"provider:" + info.Provider,
			"model:" + model,
			"streaming:" + strconv.FormatBool(info.Streaming),
			"content_encoding:" + info.ContentEncoding,
		}, 1.0)
	}
}
