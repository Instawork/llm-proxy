package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/config"
)

// HeaderModelRetired marks proxy-origin retired-model responses. The JSON body
// matches each vendor's model-not-found envelope so downstream SDKs parse it
// normally; this header identifies that the proxy short-circuited upstream.
const HeaderModelRetired = "X-LLM-Proxy-Error-Class"

const modelRetiredErrorClass = "model_retired"

// RetiredModelResponder is an optional provider capability. Providers that
// implement it return vendor-native model-not-found JSON; others fall back to
// DefaultRetiredModelError (OpenAI-compatible).
type RetiredModelResponder interface {
	FormatRetiredModelError(model string, entry config.RetiredModelEntry) (status int, body []byte, err error)
}

// FormatRetiredModelErrorForProvider dispatches to the provider's custom
// formatter when implemented, otherwise DefaultRetiredModelError.
func FormatRetiredModelErrorForProvider(provider Provider, model string, entry config.RetiredModelEntry) (int, []byte, error) {
	if r, ok := provider.(RetiredModelResponder); ok {
		return r.FormatRetiredModelError(model, entry)
	}
	return DefaultRetiredModelError(model, entry)
}

// WriteRetiredModelResponse writes a vendor-shaped model-not-found response.
func WriteRetiredModelResponse(w http.ResponseWriter, provider Provider, model string, entry config.RetiredModelEntry) error {
	status, body, err := FormatRetiredModelErrorForProvider(provider, model, entry)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(HeaderModelRetired, modelRetiredErrorClass)
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

// DefaultRetiredModelError is the OpenAI-compatible fallback for providers
// without a custom RetiredModelResponder implementation.
func DefaultRetiredModelError(model string, entry config.RetiredModelEntry) (int, []byte, error) {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": openAIRetiredMessage(model, entry),
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    "model_not_found",
		},
	})
	return http.StatusNotFound, body, err
}

func openAIRetiredMessage(model string, entry config.RetiredModelEntry) string {
	msg := fmt.Sprintf("The model `%s` has been retired", model)
	if entry.RetiredDate != "" {
		msg += fmt.Sprintf(" as of %s", entry.RetiredDate)
	}
	if entry.Replacement != "" {
		msg += fmt.Sprintf(". Migrate to `%s`", entry.Replacement)
	}
	msg += "."
	return msg
}

func anthropicRetiredMessage(model string, entry config.RetiredModelEntry) string {
	msg := fmt.Sprintf("model: %s has been retired", model)
	if entry.RetiredDate != "" {
		msg += fmt.Sprintf(" as of %s", entry.RetiredDate)
	}
	if entry.Replacement != "" {
		msg += fmt.Sprintf(". Migrate to %s", entry.Replacement)
	}
	return msg
}

func geminiRetiredMessage(model string, entry config.RetiredModelEntry) string {
	geminiModel := model
	if !strings.HasPrefix(geminiModel, "models/") {
		geminiModel = "models/" + geminiModel
	}
	msg := fmt.Sprintf("%s is not found for API version v1beta, or is not supported for generateContent", geminiModel)
	if entry.RetiredDate != "" || entry.Replacement != "" {
		msg += "."
		if entry.RetiredDate != "" {
			msg += fmt.Sprintf(" Retired as of %s.", entry.RetiredDate)
		}
		if entry.Replacement != "" {
			msg += fmt.Sprintf(" Migrate to %s.", entry.Replacement)
		}
	} else {
		msg += ". Call ListModels to see the list of available models and their supported methods."
	}
	return msg
}

func bedrockRetiredMessage(model string, entry config.RetiredModelEntry) string {
	msg := "Could not resolve the foundation model from the provided model identifier."
	if entry.RetiredDate != "" || entry.Replacement != "" {
		msg += fmt.Sprintf(" Model %q has been retired", model)
		if entry.RetiredDate != "" {
			msg += fmt.Sprintf(" as of %s", entry.RetiredDate)
		}
		if entry.Replacement != "" {
			msg += fmt.Sprintf("; migrate to %q", entry.Replacement)
		}
		msg += "."
	}
	return msg
}
