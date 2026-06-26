package fake

const (
	HeaderOutputTokens = "X-LLM-Proxy-Fake-Output-Tokens"
	HeaderChaosRate    = "X-LLM-Proxy-Fake-Chaos-Rate"
	HeaderLatencyMs    = "X-LLM-Proxy-Fake-Latency-Ms"
	HeaderOutcome      = "X-LLM-Proxy-Fake-Outcome"
	// HeaderCachedTokens injects a prompt-cache hit into the synthetic usage.
	// For OpenAI/Gemini (inclusive convention) the cached count is reported as
	// a SUBSET of prompt_tokens — it does not increase the top-level input
	// count. This lets fuzz scenarios verify the proxy does not double-count
	// cached tokens into input cost.
	HeaderCachedTokens = "X-LLM-Proxy-Fake-Cached-Tokens"
	// HeaderEchoPlaceholders makes the fake upstream echo MASK/SEAL wire
	// placeholders found in the scrubbed request body. Used by fuzz PII
	// scenarios to exercise response-restore through the real proxy stack
	// (Presidio scrub → fake upstream → restore middleware) without a live LLM.
	HeaderEchoPlaceholders = "X-LLM-Proxy-Fake-Echo-Placeholders"
	// HeaderEchoPlaceholdersFormat selects how echoed placeholders are wrapped.
	// Values: square (default wire uses angle brackets), curly, paren,
	// spaced-square. The inner token always retains the PII_ prefix.
	HeaderEchoPlaceholdersFormat = "X-LLM-Proxy-Fake-Echo-Placeholders-Format"
)
