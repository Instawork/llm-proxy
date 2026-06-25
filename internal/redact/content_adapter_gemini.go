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
	return false
}
