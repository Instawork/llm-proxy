package redact

type anthropicContentAdapter struct{}

func (anthropicContentAdapter) Provider() string { return "anthropic" }

func (anthropicContentAdapter) ScrubString(path []string, key string) bool {
	switch key {
	case "system":
		return true
	case "content":
		return hasPathAncestor(path, "messages") ||
			hasPathAncestor(path, "tool_result") ||
			hasPathAncestor(path, "output")
	case "text":
		return hasPathAncestor(path, "parts") ||
			hasPathAncestor(path, "content") ||
			hasPathAncestor(path, "message") ||
			hasPathAncestor(path, "system")
	case "input":
		return hasPathAncestor(path, "tool_use") ||
			hasPathAncestor(path, "tool_calls") ||
			hasPathAncestor(path, "function_call")
	case "arguments", "output":
		return hasPathAncestor(path, "tool_calls") || hasPathAncestor(path, "function")
	default:
		return false
	}
}

func (anthropicContentAdapter) ScrubArrayElement(path []string) bool {
	return false
}

func (anthropicContentAdapter) ScrubJSONValue(path []string, key string) bool {
	if key != "input" {
		return false
	}
	return hasPathAncestor(path, "tool_use")
}
