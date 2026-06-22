package providers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
)

func TestRetiredModelError_VendorShapes(t *testing.T) {
	entry := config.RetiredModelEntry{
		RetiredDate: "2025-10-27",
		Replacement: "o4-mini",
	}
	model := "o1-mini"

	tests := []struct {
		name     string
		provider Provider
		wantCode int
		assert   func(t *testing.T, body map[string]any)
	}{
		{
			name:     "openai",
			provider: &OpenAIProxy{},
			wantCode: http.StatusNotFound,
			assert: func(t *testing.T, body map[string]any) {
				errObj, ok := body["error"].(map[string]any)
				if !ok {
					t.Fatalf("error object missing: %#v", body)
				}
				if errObj["type"] != "invalid_request_error" {
					t.Fatalf("type=%v", errObj["type"])
				}
				if errObj["code"] != "model_not_found" {
					t.Fatalf("code=%v", errObj["code"])
				}
				msg, _ := errObj["message"].(string)
				if msg == "" || msg[0:1] != "T" {
					t.Fatalf("message=%q", msg)
				}
			},
		},
		{
			name:     "anthropic",
			provider: &AnthropicProxy{},
			wantCode: http.StatusNotFound,
			assert: func(t *testing.T, body map[string]any) {
				if body["type"] != "error" {
					t.Fatalf("type=%v", body["type"])
				}
				errObj, ok := body["error"].(map[string]any)
				if !ok {
					t.Fatalf("error object missing: %#v", body)
				}
				if errObj["type"] != "not_found_error" {
					t.Fatalf("error.type=%v", errObj["type"])
				}
				msg, _ := errObj["message"].(string)
				if msg == "" || msg[:6] != "model:" {
					t.Fatalf("message=%q", msg)
				}
			},
		},
		{
			name:     "gemini",
			provider: &GeminiProxy{},
			wantCode: http.StatusNotFound,
			assert: func(t *testing.T, body map[string]any) {
				errObj, ok := body["error"].(map[string]any)
				if !ok {
					t.Fatalf("error object missing: %#v", body)
				}
				if errObj["status"] != "NOT_FOUND" {
					t.Fatalf("status=%v", errObj["status"])
				}
				code, ok := errObj["code"].(float64)
				if !ok || int(code) != 404 {
					t.Fatalf("code=%v", errObj["code"])
				}
				msg, _ := errObj["message"].(string)
				if msg == "" || msg[:7] != "models/" {
					t.Fatalf("message=%q", msg)
				}
			},
		},
		{
			name:     "bedrock",
			provider: &BedrockProxy{},
			wantCode: http.StatusNotFound,
			assert: func(t *testing.T, body map[string]any) {
				if body["__type"] != "ResourceNotFoundException" {
					t.Fatalf("__type=%v", body["__type"])
				}
				msg, _ := body["message"].(string)
				if msg == "" {
					t.Fatalf("message empty")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, raw, err := FormatRetiredModelErrorForProvider(tc.provider, model, entry)
			if err != nil {
				t.Fatal(err)
			}
			if status != tc.wantCode {
				t.Fatalf("status=%d want %d", status, tc.wantCode)
			}
			var parsed map[string]any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				t.Fatalf("unmarshal: %v body=%s", err, raw)
			}
			tc.assert(t, parsed)
		})
	}
}

func TestDefaultRetiredModelError_UnknownProvider(t *testing.T) {
	type unknownProvider struct{}

	prov := unknownProvider{}
	if _, ok := any(prov).(Provider); ok {
		t.Fatal("unexpected Provider satisfaction")
	}

	status, raw, err := DefaultRetiredModelError("foo", config.RetiredModelEntry{Replacement: "bar"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("status=%d", status)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "model_not_found" {
		t.Fatalf("code=%v", errObj["code"])
	}
}

func TestWriteRetiredModelResponse_SetsProxyHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteRetiredModelResponse(rec, &OpenAIProxy{}, "o1-mini", config.RetiredModelEntry{
		RetiredDate: "2025-10-27",
		Replacement: "o4-mini",
	}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Header().Get(HeaderModelRetired); got != "model_retired" {
		t.Fatalf("header=%q", got)
	}
}

var (
	_ RetiredModelResponder = (*OpenAIProxy)(nil)
	_ RetiredModelResponder = (*AnthropicProxy)(nil)
	_ RetiredModelResponder = (*GeminiProxy)(nil)
	_ RetiredModelResponder = (*BedrockProxy)(nil)
)
