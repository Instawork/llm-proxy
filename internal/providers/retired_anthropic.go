package providers

import (
	"encoding/json"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/config"
)

func (p *AnthropicProxy) FormatRetiredModelError(model string, entry config.RetiredModelEntry) (int, []byte, error) {
	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    "not_found_error",
			"message": anthropicRetiredMessage(model, entry),
		},
	})
	return http.StatusNotFound, body, err
}
