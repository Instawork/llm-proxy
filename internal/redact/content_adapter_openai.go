package redact

type openAIContentAdapter struct{}

func (openAIContentAdapter) Provider() string { return "openai" }

func (openAIContentAdapter) ScrubString(path []string, key string) bool {
	switch key {
	case "content":
		return hasPathAncestor(path, "messages")
	case "text":
		return hasPathAncestor(path, "content") || hasPathAncestor(path, "input")
	case "input", "prompt":
		return len(path) == 0
	case "arguments", "output":
		// tool_calls/function covers Chat Completions; "input" covers the
		// Responses API, where multi-turn tool use resends function_call /
		// function_call_output items directly in the input array (array
		// traversal does not append path segments, so those items are seen
		// with path=["input"]).
		return hasPathAncestor(path, "tool_calls") || hasPathAncestor(path, "function") ||
			hasPathAncestor(path, "input")
	default:
		return false
	}
}

func (openAIContentAdapter) ScrubArrayElement(path []string) bool {
	return hasPathAncestor(path, "input")
}

func (openAIContentAdapter) ScrubJSONValue(path []string, key string) bool {
	return false
}
