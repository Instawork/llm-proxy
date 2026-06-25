package redact

type bedrockContentAdapter struct{}

func (bedrockContentAdapter) Provider() string { return "bedrock" }

func (bedrockContentAdapter) ScrubString(path []string, key string) bool {
	switch key {
	case "text":
		return hasPathAncestor(path, "content") ||
			hasPathAncestor(path, "system") ||
			hasPathAncestor(path, "toolResult")
	case "content":
		return hasPathAncestor(path, "tool_result") || hasPathAncestor(path, "toolResult")
	default:
		return false
	}
}

func (bedrockContentAdapter) ScrubArrayElement(path []string) bool {
	return false
}

func (bedrockContentAdapter) ScrubJSONValue(path []string, key string) bool {
	if key == "json" {
		return hasPathAncestor(path, "toolResult") && hasPathAncestor(path, "content")
	}
	if key == "input" {
		return hasPathAncestor(path, "toolUse")
	}
	return false
}
