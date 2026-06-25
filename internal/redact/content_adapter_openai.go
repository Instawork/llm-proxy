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
		return hasPathAncestor(path, "tool_calls") || hasPathAncestor(path, "function")
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
