package redact

import (
	"context"
)

type providerCtxKey struct{}

// ContentAdapter decides which JSON string (and embedded JSON blob) fields
// carry user-supplied text for a given upstream provider wire format.
type ContentAdapter interface {
	Provider() string
	ScrubString(path []string, key string) bool
	ScrubArrayElement(path []string) bool
	ScrubJSONValue(path []string, key string) bool
}

// WithProvider stashes the proxy provider name (openai, anthropic, gemini,
// bedrock) on ctx for scrubJSON / prepareJSONForAnalysis.
func WithProvider(ctx context.Context, provider string) context.Context {
	if provider == "" {
		return ctx
	}
	return context.WithValue(ctx, providerCtxKey{}, provider)
}

// ProviderFromContext returns the provider stashed by WithProvider.
func ProviderFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(providerCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// AdapterForContext resolves the ContentAdapter for ctx.
func AdapterForContext(ctx context.Context) ContentAdapter {
	return AdapterForProvider(ProviderFromContext(ctx))
}

// AdapterForProvider returns the adapter registered for name, or a union
// adapter that applies every provider's rules when name is unknown.
func AdapterForProvider(name string) ContentAdapter {
	switch name {
	case "openai":
		return openAIContentAdapter{}
	case "anthropic":
		return anthropicContentAdapter{}
	case "gemini":
		return geminiContentAdapter{}
	case "bedrock":
		return bedrockContentAdapter{}
	default:
		return unionContentAdapter{}
	}
}

func hasPathAncestor(path []string, key string) bool {
	for _, seg := range path {
		if seg == key {
			return true
		}
	}
	return false
}

type unionContentAdapter struct{}

func (unionContentAdapter) Provider() string { return "union" }

func (unionContentAdapter) ScrubString(path []string, key string) bool {
	for _, a := range allContentAdapters {
		if a.ScrubString(path, key) {
			return true
		}
	}
	return false
}

func (unionContentAdapter) ScrubArrayElement(path []string) bool {
	for _, a := range allContentAdapters {
		if a.ScrubArrayElement(path) {
			return true
		}
	}
	return false
}

func (unionContentAdapter) ScrubJSONValue(path []string, key string) bool {
	for _, a := range allContentAdapters {
		if a.ScrubJSONValue(path, key) {
			return true
		}
	}
	return false
}

var allContentAdapters = []ContentAdapter{
	openAIContentAdapter{},
	anthropicContentAdapter{},
	geminiContentAdapter{},
	bedrockContentAdapter{},
}
