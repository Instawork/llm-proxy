package providers

import (
	"regexp"
	"strings"
)

// EndpointClass says whether the proxy meters token usage for a provider path.
type EndpointClass int

const (
	// EndpointUnknown is a provider path the registry has never classified.
	// Traffic here is proxied but not billed to a key, so it is surfaced on the
	// Model Status page for triage.
	EndpointUnknown EndpointClass = iota
	// EndpointMetered paths have a ParseResponseMetadata branch and feed cost
	// tracking.
	EndpointMetered
	// EndpointPassthrough paths are known and intentionally unmetered because
	// they do not bill tokens (file uploads, model lists, token counting).
	EndpointPassthrough
)

func (c EndpointClass) String() string {
	switch c {
	case EndpointMetered:
		return "metered"
	case EndpointPassthrough:
		return "passthrough"
	default:
		return "unknown"
	}
}

// meteredSuffixes are the request paths whose responses every provider knows
// how to parse for token usage. Grouped by the vendor API that defines them.
var meteredSuffixes = []string{
	// OpenAI (also served by Anthropic, Gemini and Bedrock Mantle compat routes).
	"/chat/completions",
	"/completions",
	"/responses",
	// Anthropic Messages (also Bedrock Mantle).
	"/messages",
	// Gemini.
	":generateContent",
	":streamGenerateContent",
	"/interactions",
	// Bedrock Converse and InvokeModel (model-native bodies; see bedrock.go).
	"/converse",
	"/converse-stream",
	"/invoke",
	"/invoke-with-response-stream",
}

// passthroughSuffixes are known vendor paths that never bill tokens.
var passthroughSuffixes = []string{
	"/models",         // model listing (all vendors)
	"/files",          // Gemini / OpenAI file management
	":countTokens",    // Gemini
	"/count_tokens",   // Anthropic
	"/batches",        // Anthropic / OpenAI batch jobs; usage lands in async results
	"/client_secrets", // OpenAI Realtime bootstrap; the session itself is a websocket
}

// passthroughSegments match anywhere in the path.
var passthroughSegments = []string{
	"/upload/",   // Gemini resumable upload
	"/realtime/", // OpenAI Realtime
}

// ClassifyEndpoint reports how the proxy treats a provider request path.
func ClassifyEndpoint(path string) EndpointClass {
	path = strings.TrimSuffix(path, "/")
	for _, s := range meteredSuffixes {
		if strings.HasSuffix(path, s) {
			return EndpointMetered
		}
	}
	for _, s := range passthroughSuffixes {
		if strings.HasSuffix(path, s) {
			return EndpointPassthrough
		}
	}
	for _, s := range passthroughSegments {
		if strings.Contains(path, s) {
			return EndpointPassthrough
		}
	}
	return EndpointUnknown
}

var (
	metaNameRe = regexp.MustCompile(`^/meta/[^/]+/`)
	// Bedrock ids carry a numeric ":0" version; Gemini's ":action" suffix stays.
	modelIDRe     = regexp.MustCompile(`/models?/[^/:]+(:[0-9]+)?`)
	opaqueIDRe    = regexp.MustCompile(`/[A-Za-z0-9_-]*[0-9][A-Za-z0-9_-]{15,}`)
	modelIDPrefix = regexp.MustCompile(`^/models?/`)
)

// EndpointTemplate collapses per-request identifiers (meta names, model ids,
// resource ids) so unmetered-endpoint counters stay bounded, e.g.
// /meta/{name}/gemini/v1beta/models/{model}:generateContent.
func EndpointTemplate(path string) string {
	path = metaNameRe.ReplaceAllString(path, "/meta/{name}/")
	path = modelIDRe.ReplaceAllStringFunc(path, func(m string) string {
		return modelIDPrefix.FindString(m) + "{model}"
	})
	return opaqueIDRe.ReplaceAllString(path, "/{id}")
}
