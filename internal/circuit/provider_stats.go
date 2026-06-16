package circuit

import (
	"context"
	"strings"
)

// providerStatsAggregator is implemented by Store backends that can roll up
// per-model breaker keys for dashboard display.
type providerStatsAggregator interface {
	GetProviderStats(ctx context.Context, provider string) (*ProviderStats, error)
}

// ProviderStatsFor returns observability stats for a provider name. When the
// store supports aggregation, failures and state across all `<provider>:<model>`
// keys (plus the bare `<provider>` fallback key) are folded into one snapshot.
// Otherwise it falls back to GetStats(provider).
func ProviderStatsFor(ctx context.Context, store Store, provider string) (*ProviderStats, error) {
	if agg, ok := store.(providerStatsAggregator); ok {
		return agg.GetProviderStats(ctx, provider)
	}
	return store.GetStats(ctx, provider)
}

func providerKeyMatches(key, provider string) bool {
	return key == provider || strings.HasPrefix(key, provider+":")
}

func worseState(a, b State) State {
	if stateRank(a) >= stateRank(b) {
		return a
	}
	return b
}

func stateRank(s State) int {
	switch s {
	case StateOpen:
		return 2
	case StateHalfOpen:
		return 1
	default:
		return 0
	}
}
