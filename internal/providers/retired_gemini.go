package providers

import (
	"encoding/json"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/config"
)

func (p *GeminiProxy) FormatRetiredModelError(model string, entry config.RetiredModelEntry) (int, []byte, error) {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    404,
			"message": geminiRetiredMessage(model, entry),
			"status":  "NOT_FOUND",
		},
	})
	return http.StatusNotFound, body, err
}
