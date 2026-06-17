// Package provision mints upstream provider API keys for proxy key creation.
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

const (
	UpstreamKindOpenAIServiceAccount = "openai_service_account"
	UpstreamKindGCPAPIKey            = "gcp_api_key"
	UpstreamKindAnthropicPooled      = "anthropic_pooled"
	UpstreamKindAnthropicTiered      = "anthropic_tiered"

	TierMetered      = "metered"
	TierElevated     = "elevated"
	TierUnrestricted = "unrestricted"
)

// ProvisionRequest carries per-provider options for upstream key assignment.
type ProvisionRequest struct {
	Name string
	Tier string
}

// Result is a freshly minted upstream credential.
type Result struct {
	ActualKey    string
	UpstreamID   string
	UpstreamKind string
	ProviderMeta map[string]string
}

// Provisioner mints or assigns an upstream API key for one provider.
type Provisioner interface {
	Provision(ctx context.Context, req ProvisionRequest) (Result, error)
	Revoke(ctx context.Context, upstreamID, upstreamKind string) error
	PoolStatus(ctx context.Context) (available int, ok bool)
}

// Manager routes provision requests by provider name.
type Manager struct {
	byProvider map[string]Provisioner
	logger     *slog.Logger
}

// NewManager returns a manager with the given per-provider provisioners.
func NewManager(logger *slog.Logger, byProvider map[string]Provisioner) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if byProvider == nil {
		byProvider = map[string]Provisioner{}
	}
	return &Manager{byProvider: byProvider, logger: logger}
}

// Enabled reports whether any provider provisioner is configured.
func (m *Manager) Enabled() bool {
	return m != nil && len(m.byProvider) > 0
}

// ForProvider returns the provisioner for a provider, if configured.
func (m *Manager) ForProvider(provider string) (Provisioner, bool) {
	if m == nil {
		return nil, false
	}
	p, ok := m.byProvider[provider]
	return p, ok
}

// Provision mints an upstream key for the given provider.
func (m *Manager) Provision(ctx context.Context, provider string, req ProvisionRequest) (Result, error) {
	p, ok := m.ForProvider(provider)
	if !ok {
		return Result{}, fmt.Errorf("provisioning not configured for provider %q", provider)
	}
	res, err := p.Provision(ctx, req)
	if err != nil {
		return Result{}, err
	}
	if m.logger != nil {
		m.logger.Info(
			"provision: upstream key minted",
			"provider", provider,
			"name", req.Name,
			"tier", req.Tier,
			"upstream_kind", res.UpstreamKind,
			"upstream_id", res.UpstreamID,
		)
	}
	return res, nil
}

// Revoke best-effort revokes an upstream credential.
func (m *Manager) Revoke(ctx context.Context, provider, upstreamID, upstreamKind string) error {
	p, ok := m.ForProvider(provider)
	if !ok {
		return nil
	}
	return p.Revoke(ctx, upstreamID, upstreamKind)
}

// Status returns provisioning availability per provider for the admin API.
func (m *Manager) Status(ctx context.Context) map[string]interface{} {
	out := map[string]interface{}{
		"enabled": m != nil && m.Enabled(),
	}
	if m == nil {
		return out
	}
	providers := make(map[string]interface{}, len(m.byProvider))
	for name, p := range m.byProvider {
		entry := map[string]interface{}{"auto_provision": true}
		if n, ok := p.PoolStatus(ctx); ok {
			entry["pool_available"] = n
		}
		if tiers, ok := p.(interface {
			TierStatus() (defaultTier string, tiers []string)
		}); ok {
			defaultTier, tierNames := tiers.TierStatus()
			if len(tierNames) > 0 {
				entry["default_tier"] = defaultTier
				entry["tiers"] = tierNames
			}
		}
		providers[name] = entry
	}
	out["providers"] = providers
	return out
}

var (
	errEmptyPool   = errors.New("anthropic key pool is empty")
	errInvalidTier = errors.New("invalid anthropic provisioning tier")
)

// ErrEmptyPool is returned when the Anthropic Redis pool has no keys left.
var ErrEmptyPool = errEmptyPool

// ErrInvalidTier is returned when an unknown Anthropic tier is requested.
var ErrInvalidTier = errInvalidTier
