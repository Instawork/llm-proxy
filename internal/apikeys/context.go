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
// keyPref nil means inherit the global default.
func EffectiveRedactPII(globalEnabled bool, key *APIKey) bool {
	if key != nil && key.RedactPII != nil {
		return *key.RedactPII
	}
	return globalEnabled
}
