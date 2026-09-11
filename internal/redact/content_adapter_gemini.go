package redact

type geminiContentAdapter struct{}

func (geminiContentAdapter) Provider() string { return "gemini" }

// Every Interactions rule is anchored on the top-level `input` container (or
// is top-level itself), which native GenerateContent bodies never carry, so
// they cannot fire on native traffic.
func (geminiContentAdapter) ScrubString(path []string, key string) bool {
	switch key {
	case "text":
		return hasPathAncestor(path, "parts") ||
			hasPathAncestor(path, "content") ||
			hasPathAncestor(path, "message") ||
			hasPathAncestor(path, "input")
	case "input", "system_instruction", "systemInstruction":
		// Interactions: `input` as a bare string, system instruction as a
		// string. Native systemInstruction is an object, so ScrubString is
		// never consulted for it.
		return len(path) == 0
	case "content", "result":
		// Interactions steps: user_input.content and function_result.result
		// in their string forms.
		return hasPathAncestor(path, "input")
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
	case "arguments", "result":
		// Interactions steps: function_call.arguments (object) and
		// function_result.result (object or Content[]).
		return hasPathAncestor(path, "input")
	default:
		return false
	}
}
