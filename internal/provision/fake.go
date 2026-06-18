package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// Fake mints deterministic-looking upstream credentials for local development.
type Fake struct {
	provider    string
	defaultTier string
	tiers       []string
}

// NewFake returns a fake provisioner for a single provider.
func NewFake(provider string) *Fake {
	return &Fake{provider: strings.ToLower(strings.TrimSpace(provider))}
}

// NewFakeAnthropic returns a fake Anthropic provisioner with tier metadata for the admin UI.
func NewFakeAnthropic() *Fake {
	return &Fake{
		provider:    "anthropic",
		defaultTier: TierMetered,
		tiers:       []string{TierMetered, TierElevated, TierUnrestricted},
	}
}

func (f *Fake) Provision(_ context.Context, req ProvisionRequest) (Result, error) {
	suffix, err := randomSuffix(8)
	if err != nil {
		return Result{}, err
	}

	switch f.provider {
	case "anthropic":
		tier := strings.ToLower(strings.TrimSpace(req.Tier))
		if tier == "" {
			tier = f.defaultTier
		}
		if !containsString(f.tiers, tier) {
			return Result{}, fmt.Errorf("%w: %q", ErrInvalidTier, tier)
		}
		return Result{
			ActualKey:    fmt.Sprintf("sk-ant-fake-dev-%s-%s", tier, suffix),
			UpstreamID:   tier,
			UpstreamKind: UpstreamKindAnthropicTiered,
		}, nil
	case "openai":
		return Result{
			ActualKey:    fmt.Sprintf("sk-fake-dev-openai-%s", suffix),
			UpstreamID:   "fake-openai-" + suffix,
			UpstreamKind: UpstreamKindOpenAIServiceAccount,
		}, nil
	case "gemini":
		return Result{
			ActualKey:    fmt.Sprintf("AIza-fake-dev-gemini-%s", suffix),
			UpstreamID:   "fake-gemini-" + suffix,
			UpstreamKind: UpstreamKindGCPAPIKey,
		}, nil
	default:
		return Result{
			ActualKey:    fmt.Sprintf("sk-fake-dev-%s-%s", f.provider, suffix),
			UpstreamID:   fmt.Sprintf("fake-%s-%s", f.provider, suffix),
			UpstreamKind: "fake",
		}, nil
	}
}

func (f *Fake) Revoke(context.Context, string, string) error {
	return nil
}

func (f *Fake) PoolStatus(context.Context) (int, bool) {
	return 0, false
}

// TierStatus reports configured tier names for the admin API.
func (f *Fake) TierStatus() (defaultTier string, tiers []string) {
	if len(f.tiers) == 0 {
		return "", nil
	}
	return f.defaultTier, append([]string(nil), f.tiers...)
}

func randomSuffix(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate fake key suffix: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
