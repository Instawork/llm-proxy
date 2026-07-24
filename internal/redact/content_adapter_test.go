package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestContentAdapter_OpenAI_ResponsesInput(t *testing.T) {
	body := `{"model":"gpt-4o","input":"hello from responses"}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, openAIContentAdapter{})
	if len(tasks) != 1 || tasks[0].text != "hello from responses" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestContentAdapter_OpenAI_CompletionsPrompt(t *testing.T) {
	body := `{"model":"davinci","prompt":"finish this sentence"}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, openAIContentAdapter{})
	if len(tasks) != 1 || tasks[0].text != "finish this sentence" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestContentAdapter_OpenAI_ResponsesInputArray(t *testing.T) {
	body := `{"input":[{"type":"input_text","text":"foo"},"bare string"]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, openAIContentAdapter{})
	if len(tasks) != 2 {
		t.Fatalf("want 2 tasks, got %d: %#v", len(tasks), tasks)
	}
}

// TestContentAdapter_OpenAI_ResponsesToolItems guards the Responses API
// multi-turn tool shape: function_call arguments and function_call_output
// output resent directly in the input array must be scrubbed, not skipped.
func TestContentAdapter_OpenAI_ResponsesToolItems(t *testing.T) {
	body := `{"input":[
		{"type":"function_call","call_id":"c1","name":"save_user","arguments":"{\"ssn\":\"222-33-4444\"}"},
		{"type":"function_call_output","call_id":"c1","output":"{\"email\":\"alice.real@gmail.com\"}"}
	]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, openAIContentAdapter{})
	texts := taskTexts(tasks)
	if !containsAll(texts, "222-33-4444", "alice.real@gmail.com") {
		t.Fatalf("Responses tool arguments/output must be scrubbed, tasks = %v", texts)
	}
}

// TestContentAdapter_Gemini_ToolCallAndResponse guards Gemini tool traffic:
// functionCall.args and functionResponse.response hold arbitrary leaf keys
// (never "text"), so they must be selected as JSON scrub values or the raw
// PII goes to Google verbatim.
func TestContentAdapter_Gemini_ToolCallAndResponse(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[
		{"functionCall":{"name":"save_user","args":{"ssn":"222-33-4444"}}},
		{"functionResponse":{"name":"lookup_user","response":{"email":"alice.real@gmail.com"}}}
	]}]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, geminiContentAdapter{})
	texts := taskTexts(tasks)
	if !containsAll(texts, "222-33-4444", "alice.real@gmail.com") {
		t.Fatalf("gemini tool payloads must be scrubbed, tasks = %v", texts)
	}

	// snake_case spellings (protobuf-JSON accepts both) are covered too.
	body = `{"contents":[{"parts":[{"function_response":{"name":"f","response":{"phone":"555-867-5309"}}}]}]}`
	tasks = nil
	root = nil
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, geminiContentAdapter{})
	if !containsAll(taskTexts(tasks), "555-867-5309") {
		t.Fatalf("snake_case gemini tool payloads must be scrubbed, tasks = %v", tasks)
	}
}

func TestContentAdapter_Anthropic_SystemBlockText(t *testing.T) {
	body := `{"system":[{"type":"text","text":"You are helpful"}],"messages":[{"role":"user","content":"hi"}]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, anthropicContentAdapter{})
	texts := taskTexts(tasks)
	if !containsAll(texts, "You are helpful", "hi") {
		t.Fatalf("tasks = %v", texts)
	}
}

func TestContentAdapter_BedrockMantle_UsesUnionForAnthropicShape(t *testing.T) {
	body := `{"system":[{"type":"text","text":"You are helpful"}],"messages":[{"role":"user","content":"hi"}]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, AdapterForProvider("bedrock-mantle"))
	texts := taskTexts(tasks)
	if !containsAll(texts, "You are helpful", "hi") {
		t.Fatalf("bedrock-mantle union should scrub Anthropic fields, tasks = %v", texts)
	}
}

func TestContentAdapter_Bedrock_ConverseAndToolResult(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"text":"book appointment"}]}],"system":[{"text":"sys prompt"}]}`
	var tasks []jsonScrubTask
	var root any
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, bedrockContentAdapter{})
	texts := taskTexts(tasks)
	if !containsAll(texts, "book appointment", "sys prompt") {
		t.Fatalf("tasks = %v", texts)
	}

	body = `{"messages":[{"role":"user","content":[{"toolResult":{"toolUseId":"t1","content":[{"json":{"email":"alice@example.com"}}]}}]}]}`
	tasks = nil
	root = nil
	_ = json.Unmarshal([]byte(body), &root)
	collectJSONScrubTasks(root, nil, &tasks, bedrockContentAdapter{})
	if len(tasks) != 1 || !strings.Contains(tasks[0].text, "alice@example.com") {
		t.Fatalf("toolResult json task = %#v", tasks)
	}
}

func TestScrubJSON_ProviderScoped(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		if !strings.Contains(payload.Text, "secret prompt") {
			t.Fatalf("unexpected analyze text: %q", payload.Text)
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, err := New(Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	body := `{"model":"davinci","prompt":"secret prompt"}`
	ctx := WithProvider(context.Background(), "openai")
	res, err := r.Scrub(ctx, body, NewRegistry())
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !strings.Contains(res.Text, "secret prompt") {
		t.Fatalf("unexpected scrub result: %q", res.Text)
	}

	ctx = WithProvider(context.Background(), "gemini")
	tasks := 0
	srv2 := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		tasks++
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r2, _ := New(Config{AnalyzerURL: srv2.URL})
	if _, err := r2.Scrub(ctx, body, NewRegistry()); err != nil {
		t.Fatalf("Scrub gemini: %v", err)
	}
	if tasks != 0 {
		t.Fatalf("gemini adapter should not scrub openai prompt, got %d analyze calls", tasks)
	}
}

func taskTexts(tasks []jsonScrubTask) []string {
	out := make([]string, len(tasks))
	for i, task := range tasks {
		out[i] = task.text
	}
	return out
}

func containsAll(haystack []string, needles ...string) bool {
	joined := strings.Join(haystack, "\n")
	for _, n := range needles {
		if !strings.Contains(joined, n) {
			return false
		}
	}
	return true
}
