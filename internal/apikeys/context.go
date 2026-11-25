package apikeys

import "context"

type contextKey struct{}

// WithContext stores the resolved proxy key record on the request context.
func WithContext(ctx context.Context, key *APIKey) context.Context {
	if key == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, key)
}

// FromContext returns the resolved proxy key record, if any.
func FromContext(ctx context.Context) (*APIKey, bool) {
	v, ok := ctx.Value(contextKey{}).(*APIKey)
	return v, ok
}

// EffectiveRedactPII returns whether PII redaction should run for this request.
// When allowPerKeyOverride is false, any per-key RedactPII setting is ignored
// and globalEnabled is always enforced. When allowPerKeyOverride is true, a
// non-nil key.RedactPII overrides globalEnabled; nil inherits globalEnabled.
func EffectiveRedactPII(globalEnabled, allowPerKeyOverride bool, key *APIKey) bool {
	if allowPerKeyOverride && key != nil && key.RedactPII != nil {
		return *key.RedactPII
	}
	return globalEnabled
}

// EffectiveAllowStreaming returns whether streaming may be used when wire-mode
// PII scrubbing is active. nil on the key inherits the global default.
func EffectiveAllowStreaming(globalDefault bool, key *APIKey) bool {
	if key != nil && key.AllowStreaming != nil {
		return *key.AllowStreaming
	}
	return globalDefault
}
