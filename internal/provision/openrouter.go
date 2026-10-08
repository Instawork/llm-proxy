package provision

import "context"

// OpenRouterShared assigns the single shared OpenRouter API key to every proxy key.
type OpenRouterShared struct {
	key string
}

// NewOpenRouterShared returns a provisioner that hands out the given key.
func NewOpenRouterShared(key string) *OpenRouterShared {
	return &OpenRouterShared{key: key}
}

func (o *OpenRouterShared) Provision(context.Context, ProvisionRequest) (Result, error) {
	return Result{
		ActualKey:    o.key,
		UpstreamID:   "shared",
		UpstreamKind: UpstreamKindOpenRouterShared,
	}, nil
}

func (o *OpenRouterShared) Rename(context.Context, string, string, string) (Result, error) {
	return Result{}, nil
}

// Revoke is a no-op: the shared key is still in use by every other proxy key.
func (o *OpenRouterShared) Revoke(context.Context, string, string) error { return nil }

func (o *OpenRouterShared) PoolStatus(context.Context) (int, bool) { return 0, false }
