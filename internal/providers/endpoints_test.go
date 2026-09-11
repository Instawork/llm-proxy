package providers

import "testing"

func TestClassifyEndpoint(t *testing.T) {
	cases := map[string]EndpointClass{
		"/openai/v1/chat/completions":                            EndpointMetered,
		"/openai/v1/responses":                                   EndpointMetered,
		"/anthropic/v1/messages":                                 EndpointMetered,
		"/gemini/v1beta/models/gemini-3.6-flash:generateContent": EndpointMetered,
		"/bedrock/model/anthropic.claude-3/converse-stream":      EndpointMetered,
		"/openai/v1/chat/completions/":                           EndpointMetered,

		"/gemini/v1beta/models":               EndpointPassthrough,
		"/gemini/upload/v1beta/files":         EndpointPassthrough,
		"/anthropic/v1/messages/count_tokens": EndpointPassthrough,
		"/openai/v1/realtime/client_secrets":  EndpointPassthrough,

		"/gemini/v1beta/interactions":              EndpointUnknown,
		"/openai/v1/audio/transcriptions":          EndpointUnknown,
		"/openai/v1/embeddings":                    EndpointUnknown,
		"/bedrock/model/anthropic.claude-3/invoke": EndpointUnknown,
	}
	for path, want := range cases {
		if got := ClassifyEndpoint(path); got != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}

func TestEndpointTemplate(t *testing.T) {
	cases := map[string]string{
		"/meta/autolabel/gemini/v1beta/models/gemini-3.8-flash:generateContent": "/meta/{name}/gemini/v1beta/models/{model}:generateContent",
		"/bedrock/model/anthropic.claude-3-5-sonnet-20241022-v2:0/invoke":       "/bedrock/model/{model}/invoke",
		"/openai/v1/responses/resp_0a1b2c3d4e5f6g7h8i9j":                        "/openai/v1/responses/{id}",
		"/gemini/v1beta/interactions":                                           "/gemini/v1beta/interactions",
	}
	for path, want := range cases {
		if got := EndpointTemplate(path); got != want {
			t.Errorf("%s: got %s, want %s", path, got, want)
		}
	}
}
