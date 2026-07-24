package redact

type geminiContentAdapter struct{}

func (geminiContentAdapter) Provider() string { return "gemini" }

func (geminiContentAdapter) ScrubString(path []string, key string) bool {
	switch key {
	case "text":
		return hasPathAncestor(path, "parts") ||
			hasPathAncestor(path, "content") ||
			hasPathAncestor(path, "message")
	default:
		return false
	}
}

func (geminiContentAdapter) ScrubArrayElement(path []string) bool {
	return false
}

func (geminiContentAdapter) ScrubJSONValue(path []string, key string) bool {
	// Tool traffic lives in contents[].parts[].functionCall.args and
	// contents[].parts[].functionResponse.response — objects whose leaf keys
	// are arbitrary (email, query, ...), never "text", so ScrubString cannot
	// catch them. Gemini's protobuf-JSON accepts both camelCase and
	// snake_case field names, so match both spellings.
	switch key {
	case "args":
		return hasPathAncestor(path, "functionCall") || hasPathAncestor(path, "function_call")
	case "response":
		return hasPathAncestor(path, "functionResponse") || hasPathAncestor(path, "function_response")
	default:
		return false
	}
}
