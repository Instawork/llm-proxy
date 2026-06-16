package circuit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProviderStatsFor_FallsBackToGetStats(t *testing.T) {
	store := &fakeProviderStatsStore{
		providerStats: &ProviderStats{Failures: 9, State: StateOpen},
		keyStats:      &ProviderStats{Failures: 1, State: StateClosed},
	}

	got, err := ProviderStatsFor(context.Background(), store, "openai")
	require.NoError(t, err)
	require.Equal(t, 9, got.Failures)
	require.Equal(t, StateOpen, got.State)
}

type fakeProviderStatsStore struct {
	providerStats *ProviderStats
	keyStats      *ProviderStats
}

func (f *fakeProviderStatsStore) GetState(context.Context, string) (State, error) {
	return StateClosed, nil
}
func (f *fakeProviderStatsStore) RecordTerminalFailure(context.Context, string) (State, bool, error) {
	return StateClosed, false, nil
}
func (f *fakeProviderStatsStore) RecordSuccess(context.Context, string) error { return nil }
func (f *fakeProviderStatsStore) RecordProbeFailed(context.Context, string) error {
	return nil
}
func (f *fakeProviderStatsStore) ForceOpen(context.Context, string, int) error {
	return nil
}
func (f *fakeProviderStatsStore) GetStats(context.Context, string) (*ProviderStats, error) {
	return f.keyStats, nil
}
func (f *fakeProviderStatsStore) GetProviderStats(context.Context, string) (*ProviderStats, error) {
	return f.providerStats, nil
}

var _ Store = (*fakeProviderStatsStore)(nil)
