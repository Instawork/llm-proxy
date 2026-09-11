package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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

func wireStackGeminiInteractionsResponse(echo string) string {
	return fmt.Sprintf(
		`{"object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"%s"}]}]}`,
		echo,
	)
}

// wireStackGeminiInteractionsCase covers the Interactions API's simplest
// shape: a bare-string `input`.
func wireStackGeminiInteractionsCase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "gemini-interactions",
		path: "/gemini/v1beta/interactions",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"model":"gemini-2.5-flash","input":"My email is %s. Reply with ONLY that email."}`,
				email,
			)
		},
		upstreamText: geminiInteractionsInputFromBody,
		responseBody: wireStackGeminiInteractionsResponse,
	}
}

// wireStackGeminiInteractionsToolCase puts the PII under an arbitrary leaf
// key of a function_result payload, which only the ScrubJSONValue blob rule
// can reach.
func wireStackGeminiInteractionsToolCase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "gemini-interactions-tool-result",
		path: "/gemini/v1beta/interactions",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"model":"gemini-2.5-flash","input":[{"type":"function_call","id":"fc1","name":"lookup","arguments":{"q":"latest"}},{"type":"function_result","call_id":"fc1","result":{"note":"My email is %s. Reply with ONLY that email."}}]}`,
				email,
			)
		},
		upstreamText: geminiInteractionsToolResultNoteFromBody,
		responseBody: wireStackGeminiInteractionsResponse,
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

func geminiInteractionsInputFromBody(body []byte) string {
	var root struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	return root.Input
}

func geminiInteractionsToolResultNoteFromBody(body []byte) string {
	var root struct {
		Input []struct {
			Result struct {
				Note string `json:"note"`
			} `json:"result"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return ""
	}
	for _, step := range root.Input {
		if step.Result.Note != "" {
			return step.Result.Note
		}
	}
	return ""
}

type wireDetection struct {
	value      string
	entityType string
}

func wireTestRedactorWithDetections(t *testing.T, detections []wireDetection) PIIRedactor {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		spans := make([]redact.Span, 0, len(detections))
		for _, d := range detections {
			idx := strings.Index(payload.Text, d.value)
			if idx < 0 {
				continue
			}
			start := utf8.RuneCountInString(payload.Text[:idx])
			end := start + utf8.RuneCountInString(d.value)
			spans = append(spans, redact.Span{
				Start:      start,
				End:        end,
				EntityType: d.entityType,
				Score:      0.95,
			})
		}
		_ = json.NewEncoder(w).Encode(spans)
	}))
	t.Cleanup(srv.Close)
	r, err := redact.New(redact.Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}
	return r
}

func wireStackOpenAINamePrompt(prompt string) func(string) string {
	return func(_ string) string {
		return fmt.Sprintf(
			`{"model":"gpt-4o-mini","max_tokens":40,"messages":[{"role":"user","content":%q}]}`,
			prompt,
		)
	}
}

func wireStackAnthropicNamePrompt(prompt string) func(string) string {
	return func(_ string) string {
		return fmt.Sprintf(
			`{"model":"claude-haiku-4-5","max_tokens":40,"messages":[{"role":"user","content":%q}]}`,
			prompt,
		)
	}
}

func wireStackGeminiNamePrompt(prompt string) func(string) string {
	return func(_ string) string {
		return fmt.Sprintf(
			`{"contents":[{"parts":[{"text":%q}]}]}`,
			prompt,
		)
	}
}

func wireStackGeminiInteractionsNamePrompt(prompt string) func(string) string {
	return func(_ string) string {
		return fmt.Sprintf(`{"model":"gemini-2.5-flash","input":%q}`, prompt)
	}
}

func wireStackGeminiInteractionsToolNamePrompt(prompt string) func(string) string {
	return func(_ string) string {
		return fmt.Sprintf(
			`{"model":"gemini-2.5-flash","input":[{"type":"function_result","call_id":"fc1","result":{"note":%q}}]}`,
			prompt,
		)
	}
}
