package providers

import (
	"encoding/json"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/config"
)

func (p *OpenAIProxy) FormatRetiredModelError(model string, entry config.RetiredModelEntry) (int, []byte, error) {
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
