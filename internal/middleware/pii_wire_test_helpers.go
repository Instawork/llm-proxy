package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

func piiMetricFromResponse(rec *httptest.ResponseRecorder, name string) string {
	if v := rec.Header().Get(name); v != "" {
		return v
	}
	return rec.Result().Trailer.Get(name)
}

func productionPIIWireStack(pm *providers.ProviderManager, handler http.Handler) http.Handler {
	return TokenParsingMiddleware(pm)(
		PIIResponseRestoreMiddleware(pm)(
			StreamingMiddleware(pm)(handler),
		),
	)
}

func wireTestProviderManager(t *testing.T) *providers.ProviderManager {
	t.Helper()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	pm.RegisterProvider(providers.NewAnthropicProxy())
	pm.RegisterProvider(providers.NewGeminiProxy())
	return pm
}

func withPIIRegistry(ctx context.Context, reg *redact.Registry) context.Context {
	return context.WithValue(ctx, piiRegistryCtxKey{}, reg)
}

func wireStackEmailScrubRedactor(email string) func(string, *redact.Registry) (redact.Result, error) {
	return func(in string, reg *redact.Registry) (redact.Result, error) {
		ph := reg.Placeholder("EMAIL_ADDRESS", email)
		out := strings.Replace(in, email, ph, 1)
		return redact.Result{
			Text:         out,
			EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
		}, nil
	}
}

type wireStackProviderCase struct {
	name         string
	path         string
	requestBody  func(email string) string
	upstreamText func(body []byte) string
	responseBody func(echo string) string
}

func wireStackOpenAICase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "openai",
		path: "/openai/v1/chat/completions",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"model":"gpt-4o-mini","max_tokens":40,"messages":[{"role":"user","content":"My email is %s. Reply with ONLY that email."}]}`,
				email,
			)
		},
		upstreamText: userContentFromChatBody,
		responseBody: func(echo string) string {
			return fmt.Sprintf(`{"choices":[{"message":{"content":"%s"}}]}`, echo)
		},
	}
}

func wireStackAnthropicCase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "anthropic",
		path: "/anthropic/v1/messages",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"model":"claude-haiku-4-5","max_tokens":40,"messages":[{"role":"user","content":"My email is %s. Reply with ONLY that email."}]}`,
				email,
			)
		},
		upstreamText: anthropicUserTextFromBody,
		responseBody: func(echo string) string {
			return fmt.Sprintf(`{"content":[{"type":"text","text":"%s"}]}`, echo)
		},
	}
}

func wireStackGeminiCase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "gemini",
		path: "/gemini/v1beta/models/gemini-2.5-flash:generateContent",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"contents":[{"parts":[{"text":"My email is %s. Reply with ONLY that email."}]}]}`,
				email,
			)
		},
		upstreamText: geminiUserTextFromBody,
		responseBody: func(echo string) string {
			return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"%s"}]}}]}`, echo)
		},
	}
}

func userContentFromChatBody(body []byte) string {
	var root struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &root); err != nil || len(root.Messages) == 0 {
		return ""
	}
	return root.Messages[0].Content
}

func anthropicUserTextFromBody(body []byte) string {
	return userContentFromChatBody(body)
}

func geminiUserTextFromBody(body []byte) string {
	var root struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &root); err != nil || len(root.Contents) == 0 || len(root.Contents[0].Parts) == 0 {
		return ""
	}
	return root.Contents[0].Parts[0].Text
}
