package providers

import (
	"encoding/json"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/config"
)

func (p *BedrockProxy) FormatRetiredModelError(model string, entry config.RetiredModelEntry) (int, []byte, error) {
	body, err := json.Marshal(map[string]string{
		"__type":  "ResourceNotFoundException",
		"message": bedrockRetiredMessage(model, entry),
	})
	return http.StatusNotFound, body, err
}
