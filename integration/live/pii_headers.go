package live

import (
	"net/http"
	"strconv"
)

// PIIMaskLeaked parses X-LLM-PII-Leaked from response headers (preferred) or
// legacy trailers.
// ok is false when the metric was not present.
func PIIMaskLeaked(headers, trailer http.Header) (leaked int, ok bool) {
	if headers != nil {
		if v := headers.Get("X-LLM-PII-Leaked"); v != "" {
			n, err := strconv.Atoi(v)
			return n, err == nil
		}
	}
	if trailer != nil {
		if v := trailer.Get("X-LLM-PII-Leaked"); v != "" {
			n, err := strconv.Atoi(v)
			return n, err == nil
		}
	}
	return 0, false
}
