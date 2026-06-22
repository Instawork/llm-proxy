package middleware

import (
	"encoding/json"
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
				writeRetiredResponse(w, providerName, model, entry)
				return
			}

			modelCfg, _ := cfg.GetModelConfig(providerName, model)
			if modelCfg != nil && modelCfg.Deprecated {
				recorder.RecordDeprecated(providerName, model)
				emitModelMetric(metrics, "model.deprecated_call", providerName, model)
			} else if modelCfg == nil {
				log.Printf("model status: unrecognized model %q for provider %q", model, providerName)
				emitModelMetric(metrics, "model.unknown_call", providerName, model)
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

func retiredMessage(model string, entry config.RetiredModelEntry) string {
	msg := fmt.Sprintf("The model '%s' has been retired", model)
	if entry.RetiredDate != "" {
		msg += fmt.Sprintf(" as of %s", entry.RetiredDate)
	}
	if entry.Replacement != "" {
		msg += fmt.Sprintf(". Migrate to '%s'", entry.Replacement)
	}
	msg += "."
	return msg
}

func writeRetiredResponse(w http.ResponseWriter, providerName, model string, entry config.RetiredModelEntry) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	body, err := formatRetiredError(providerName, model, entry)
	if err != nil {
		log.Printf("model status: failed to encode retired response: %v", err)
		fmt.Fprintf(w, `{"error":"model retired"}`)
		return
	}
	_, _ = w.Write(body)
}

func formatRetiredError(providerName, model string, entry config.RetiredModelEntry) ([]byte, error) {
	msg := retiredMessage(model, entry)
	switch providerName {
	case "anthropic":
		return json.Marshal(map[string]interface{}{
			"type": "error",
			"error": map[string]string{
				"type":    "not_found_error",
				"message": msg,
			},
		})
	case "gemini":
		return json.Marshal(map[string]interface{}{
			"error": map[string]interface{}{
				"code":    410,
				"message": msg,
				"status":  "GONE",
			},
		})
	case "bedrock":
		return json.Marshal(map[string]string{
			"message": msg,
		})
	default:
		payload := map[string]interface{}{
			"error": map[string]interface{}{
				"message": msg,
				"type":    "invalid_request_error",
				"param":   "model",
				"code":    "model_retired",
			},
		}
		if entry.Replacement != "" {
			payload["error"].(map[string]interface{})["replacement"] = entry.Replacement
		}
		return json.Marshal(payload)
	}
}
