package middleware

import (
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// RequestMemoMiddleware attaches a providers.RequestMemo to the request
// context so every later middleware shares one parse of the request body for
// the stream flag, model, messages, and user ID. Mount it before any
// middleware that inspects the body.
func RequestMemoMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(providers.WithRequestMemo(r.Context())))
		})
	}
}
