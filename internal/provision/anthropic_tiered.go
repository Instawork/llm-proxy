package provision

import (
	"context"
	"fmt"
	"strings"
)

// AnthropicTiered assigns shared Anthropic API keys by trust tier.
type AnthropicTiered struct {
	defaultTier string
	keys        map[string]string
}

// NewAnthropicTiered returns a tiered Anthropic provisioner.
func NewAnthropicTiered(defaultTier string, keys map[string]string) (*AnthropicTiered, error) {
	if defaultTier == "" {
		defaultTier = TierMetered
	}
	clean := make(map[string]string, len(keys))
	for tier, key := range keys {
		tier = strings.ToLower(strings.TrimSpace(tier))
		key = strings.TrimSpace(key)
		if tier == "" || key == "" {
			continue
		}
		clean[tier] = key
	}
	if len(clean) == 0 {
		return nil, fmt.Errorf("anthropic tiered provisioner: no tier keys configured")
	}
	if _, ok := clean[defaultTier]; !ok {
		return nil, fmt.Errorf("anthropic tiered provisioner: default tier %q is not configured", defaultTier)
	}
	return &AnthropicTiered{defaultTier: defaultTier, keys: clean}, nil
}

func (a *AnthropicTiered) Provision(ctx context.Context, req ProvisionRequest) (Result, error) {
	_ = ctx
	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	if tier == "" {
		tier = a.defaultTier
	}
	key, ok := a.keys[tier]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q", ErrInvalidTier, tier)
	}
	return Result{
		ActualKey:    key,
		UpstreamID:   tier,
		UpstreamKind: UpstreamKindAnthropicTiered,
	}, nil
}

func (a *AnthropicTiered) Revoke(ctx context.Context, upstreamID, upstreamKind string) error {
	_ = ctx
	_ = upstreamID
	if upstreamKind == UpstreamKindAnthropicTiered {
		return nil
	}
	return nil
}

func (a *AnthropicTiered) PoolStatus(context.Context) (int, bool) { return 0, false }

// TierStatus reports configured tier names for the admin API.
func (a *AnthropicTiered) TierStatus() (defaultTier string, tiers []string) {
	tiers = make([]string, 0, len(a.keys))
	for tier := range a.keys {
		tiers = append(tiers, tier)
	}
	return a.defaultTier, tiers
}
