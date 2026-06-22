package middleware

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/modelstatusstats"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// ModelStatusMiddleware short-circuits requests to retired models and records
// deprecated-model usage before forwarding to upstream providers.
func ModelStatusMiddleware(
	pm *providers.ProviderManager,
	cfg *config.YAMLConfig,
	recorder *modelstatusstats.Recorder,
	metrics circuit.MetricsSink,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/redact" || strings.HasPrefix(r.URL.Path, "/admin/") {
				next.ServeHTTP(w, r)
				return
			}

			provider := GetProviderFromRequest(pm, r)
			if provider == nil {
				next.ServeHTTP(w, r)
				return
			}

			model, _ := provider.ExtractRequestModelAndMessages(r)
			if model == "" {
				next.ServeHTTP(w, r)
				return
			}

			providerName := provider.GetName()
			if entry, retired := cfg.LookupRetiredModel(providerName, model); retired {
				recorder.RecordRetired(providerName, model)
				emitModelMetric(metrics, "model.retired_call", providerName, model)
				if err := providers.WriteRetiredModelResponse(w, provider, model, entry); err != nil {
					log.Printf("model status: failed to encode retired response: %v", err)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set(providers.HeaderModelRetired, "model_retired")
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprintf(w, `{"error":"model retired"}`)
				}
				return
			}

			modelCfg, _ := cfg.GetModelConfig(providerName, model)
			if modelCfg != nil && modelCfg.Deprecated {
				recorder.RecordDeprecated(providerName, model)
				emitModelMetric(metrics, "model.deprecated_call", providerName, model)
			} else if modelCfg == nil {
				recorder.RecordUnknown(providerName, model)
				log.Printf("model status: unrecognized model %q for provider %q", model, providerName)
				emitModelMetric(metrics, "model.unknown_call", providerName, "__unknown__")
			}

			next.ServeHTTP(w, r)
		})
	}
}

func emitModelMetric(metrics circuit.MetricsSink, name, provider, model string) {
	if metrics == nil {
		return
	}
	tags := []string{
		"provider:" + provider,
		"model:" + model,
	}
	_ = metrics.Incr(name, tags, 1)
}
