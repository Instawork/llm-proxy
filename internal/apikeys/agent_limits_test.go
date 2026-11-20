package apikeys

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/config"
)

func agentLimitsTestConfig() config.AgentSpendLimitsConfig {
	return config.AgentSpendLimitsConfig{
		Enabled:                 true,
		Mode:                    config.AgentSpendModeObserve,
		UserPrefix:              "finch-agent-",
		DefaultSoftMonthlyCents: 10000,
		DefaultHardMonthlyCents: 20000,
		MaxHardMonthlyCents:     500000,
		LimitCacheTTLSeconds:    60,
	}
}

func TestAgentLimit_PutGetClearDelete(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	const label = "finch-agent-123"

	_, err := store.GetAgentLimit(ctx, label)
	require.ErrorIs(t, err, ErrAgentLimitNotFound)

	rec, err := store.PutAgentLimit(ctx, label, AgentLimitUpdate{
		SoftMonthlyCents: SetField[int64](5000),
		HardMonthlyCents: SetField[int64](30000),
		Exempt:           SetField(true),
		UpdatedBy:        "Owner@Example.com",
	})
	require.NoError(t, err)
	assert.Equal(t, label, rec.Label())
	require.NotNil(t, rec.SoftMonthlyCents)
	assert.Equal(t, int64(5000), *rec.SoftMonthlyCents)
	assert.Equal(t, "owner@example.com", rec.UpdatedBy)
	assert.False(t, rec.UpdatedAt.IsZero())

	// Clearing soft and exempt leaves hard untouched.
	rec, err = store.PutAgentLimit(ctx, label, AgentLimitUpdate{
		SoftMonthlyCents: ClearField[int64](),
		Exempt:           ClearField[bool](),
		UpdatedBy:        "admin@example.com",
	})
	require.NoError(t, err)
	assert.Nil(t, rec.SoftMonthlyCents)
	assert.Nil(t, rec.Exempt)
	require.NotNil(t, rec.HardMonthlyCents)
	assert.Equal(t, int64(30000), *rec.HardMonthlyCents)

	got, err := store.GetAgentLimit(ctx, label)
	require.NoError(t, err)
	assert.Equal(t, label, got.Label())
	assert.Nil(t, got.SoftMonthlyCents)
	assert.Nil(t, got.Exempt)
	require.NotNil(t, got.HardMonthlyCents)
	assert.Equal(t, int64(30000), *got.HardMonthlyCents)
	assert.Equal(t, "admin@example.com", got.UpdatedBy)

	eff := MergeAgentLimit(agentLimitsTestConfig(), label, got)
	assert.Equal(t, int64(10000), eff.SoftMonthlyCents, "cleared soft falls back to the default")
	assert.Equal(t, int64(30000), eff.HardMonthlyCents)
	assert.False(t, eff.Exempt)

	require.NoError(t, store.DeleteAgentLimit(ctx, label))
	_, err = store.GetAgentLimit(ctx, label)
	require.ErrorIs(t, err, ErrAgentLimitNotFound)
	require.NoError(t, store.DeleteAgentLimit(ctx, label), "deleting a missing item is not an error")
}

func TestAgentLimit_PutRejectsBadInput(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	_, err := store.PutAgentLimit(ctx, "finch-agent-1", AgentLimitUpdate{SoftMonthlyCents: SetField[int64](1)})
	require.ErrorContains(t, err, "updated_by")

	_, err = store.PutAgentLimit(ctx, "finch-agent-1", AgentLimitUpdate{
		HardMonthlyCents: SetField[int64](-1),
		UpdatedBy:        "a@example.com",
	})
	require.ErrorContains(t, err, "negative")

	for _, label := range []string{"", "finch agent", "finch-agent-1/x", "finch-agent-1|spend_usd", "a#b"} {
		_, err := store.PutAgentLimit(ctx, label, AgentLimitUpdate{UpdatedBy: "a@example.com"})
		require.ErrorIs(t, err, ErrInvalidAgentLabel, label)
		_, err = store.GetAgentLimit(ctx, label)
		require.ErrorIs(t, err, ErrInvalidAgentLabel, label)
	}
}

func TestAgentLimit_NotReturnedByOtherScans(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	_, err := store.CreateKey(ctx, "openai", "real-sk", "a key", 0, nil, nil)
	require.NoError(t, err)
	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "req@example.com",
		Provider:       "openai",
		Name:           "svc",
		Description:    "needed",
	})
	require.NoError(t, err)
	_, err = store.PutAgentLimit(ctx, "finch-agent-7", AgentLimitUpdate{
		HardMonthlyCents: SetField[int64](1000),
		UpdatedBy:        "a@example.com",
	})
	require.NoError(t, err)

	keys, err := store.ListKeys(ctx, "")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.NotContains(t, keys[0].PK, AgentLimitPrefix)

	reqs, err := store.ListKeyRequests(ctx, "")
	require.NoError(t, err)
	require.Len(t, reqs, 1)

	bans, err := store.ListBYOBans(ctx, "")
	require.NoError(t, err)
	assert.Empty(t, bans)
}

func TestEffectiveAgentLimits_Check(t *testing.T) {
	const maxHard = 500000
	cases := []struct {
		name       string
		soft, hard int64
		wantErr    string
	}{
		{"defaults", 10000, 20000, ""},
		{"equal", 5000, 5000, ""},
		{"zero", 0, 0, ""},
		{"at max", 0, maxHard, ""},
		{"negative soft", -1, 100, "negative"},
		{"hard below soft", 300, 200, "at least"},
		{"over max", 0, maxHard + 1, "exceed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := EffectiveAgentLimits{SoftMonthlyCents: tc.soft, HardMonthlyCents: tc.hard}.Check(maxHard)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

type fakeAgentLimitGetter struct {
	mu    sync.Mutex
	calls int
	items map[string]*AgentLimit
	err   error
}

func (f *fakeAgentLimitGetter) GetAgentLimit(_ context.Context, label string) (*AgentLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	rec, ok := f.items[label]
	if !ok {
		return nil, ErrAgentLimitNotFound
	}
	return rec.clone(), nil
}

func (f *fakeAgentLimitGetter) set(label string, rec *AgentLimit, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rec != nil {
		f.items[label] = rec
	} else {
		delete(f.items, label)
	}
	f.err = err
}

func (f *fakeAgentLimitGetter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestResolver(getter AgentLimitGetter) (*AgentLimitResolver, *time.Time) {
	r := NewAgentLimitResolver(getter, agentLimitsTestConfig())
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	return r, &now
}

func ptr[T any](v T) *T { return &v }

func TestAgentLimitResolver_CachesHitsUntilTTL(t *testing.T) {
	getter := &fakeAgentLimitGetter{items: map[string]*AgentLimit{
		"finch-agent-1": {PK: AgentLimitPrefix + "finch-agent-1", HardMonthlyCents: ptr[int64](50000), Exempt: ptr(true)},
	}}
	r, now := newTestResolver(getter)
	ctx := context.Background()

	eff, err := r.Resolve(ctx, "finch-agent-1")
	require.NoError(t, err)
	assert.Equal(t, int64(10000), eff.SoftMonthlyCents)
	assert.Equal(t, int64(50000), eff.HardMonthlyCents)
	assert.True(t, eff.Exempt)
	require.NotNil(t, eff.Override)

	getter.set("finch-agent-1", &AgentLimit{PK: AgentLimitPrefix + "finch-agent-1", HardMonthlyCents: ptr[int64](1000)}, nil)
	*now = now.Add(59 * time.Second)
	eff, err = r.Resolve(ctx, "finch-agent-1")
	require.NoError(t, err)
	assert.Equal(t, int64(50000), eff.HardMonthlyCents, "served from cache within the TTL")
	assert.Equal(t, 1, getter.callCount())

	*now = now.Add(time.Second)
	eff, err = r.Resolve(ctx, "finch-agent-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1000), eff.HardMonthlyCents, "re-read after the TTL")
	assert.False(t, eff.Exempt)
	assert.Equal(t, 2, getter.callCount())
}

func TestAgentLimitResolver_CachesMisses(t *testing.T) {
	getter := &fakeAgentLimitGetter{items: map[string]*AgentLimit{}}
	r, now := newTestResolver(getter)
	ctx := context.Background()

	for range 3 {
		eff, err := r.Resolve(ctx, "finch-agent-2")
		require.NoError(t, err)
		assert.Equal(t, int64(10000), eff.SoftMonthlyCents)
		assert.Equal(t, int64(20000), eff.HardMonthlyCents)
		assert.Nil(t, eff.Override)
	}
	assert.Equal(t, 1, getter.callCount(), "a miss is cached like a hit")

	getter.set("finch-agent-2", &AgentLimit{PK: AgentLimitPrefix + "finch-agent-2", SoftMonthlyCents: ptr[int64](0)}, nil)
	*now = now.Add(time.Minute)
	eff, err := r.Resolve(ctx, "finch-agent-2")
	require.NoError(t, err)
	assert.Equal(t, int64(0), eff.SoftMonthlyCents, "an explicit 0 overrides the default")
}

func TestAgentLimitResolver_ErrorsAreNotCachedAndServeStale(t *testing.T) {
	getter := &fakeAgentLimitGetter{items: map[string]*AgentLimit{
		"finch-agent-3": {PK: AgentLimitPrefix + "finch-agent-3", HardMonthlyCents: ptr[int64](70000)},
	}}
	r, now := newTestResolver(getter)
	ctx := context.Background()
	boom := errors.New("dynamodb unavailable")

	_, err := r.Resolve(ctx, "finch-agent-3")
	require.NoError(t, err)

	getter.set("finch-agent-3", getter.items["finch-agent-3"], boom)
	*now = now.Add(2 * time.Minute)
	eff, err := r.Resolve(ctx, "finch-agent-3")
	require.ErrorIs(t, err, boom)
	assert.Equal(t, int64(70000), eff.HardMonthlyCents, "stale override is served on error")

	eff, err = r.Resolve(ctx, "finch-agent-4")
	require.ErrorIs(t, err, boom)
	assert.Equal(t, int64(20000), eff.HardMonthlyCents, "defaults are served on error with no cache entry")

	calls := getter.callCount()
	_, err = r.Resolve(ctx, "finch-agent-4")
	require.ErrorIs(t, err, boom)
	assert.Equal(t, calls+1, getter.callCount(), "errors are not cached")

	getter.set("finch-agent-4", nil, nil)
	_, err = r.Resolve(ctx, "finch-agent-4")
	require.NoError(t, err)
}

func TestAgentLimitResolver_InvalidateAndDefensiveCopies(t *testing.T) {
	getter := &fakeAgentLimitGetter{items: map[string]*AgentLimit{
		"finch-agent-5": {PK: AgentLimitPrefix + "finch-agent-5", SoftMonthlyCents: ptr[int64](100)},
	}}
	r, _ := newTestResolver(getter)
	ctx := context.Background()

	eff, err := r.Resolve(ctx, "finch-agent-5")
	require.NoError(t, err)
	*eff.Override.SoftMonthlyCents = 999

	eff, err = r.Resolve(ctx, "finch-agent-5")
	require.NoError(t, err)
	assert.Equal(t, int64(100), eff.SoftMonthlyCents, "mutating a result does not touch the cache")

	r.Invalidate("finch-agent-5")
	_, err = r.Resolve(ctx, "finch-agent-5")
	require.NoError(t, err)
	assert.Equal(t, 2, getter.callCount())
}

func TestAgentLimitResolver_ConcurrentResolve(t *testing.T) {
	getter := &fakeAgentLimitGetter{items: map[string]*AgentLimit{}}
	r := NewAgentLimitResolver(getter, agentLimitsTestConfig())
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			label := "finch-agent-" + string(rune('a'+i%4))
			for range 50 {
				_, err := r.Resolve(ctx, label)
				assert.NoError(t, err)
				if i%5 == 0 {
					r.Invalidate(label)
				}
			}
		})
	}
	wg.Wait()
}

func TestAgentLimitResolver_DefaultTTL(t *testing.T) {
	cfg := agentLimitsTestConfig()
	cfg.LimitCacheTTLSeconds = 0
	r := NewAgentLimitResolver(&fakeAgentLimitGetter{items: map[string]*AgentLimit{}}, cfg)
	assert.Equal(t, 60*time.Second, r.ttl)
	assert.Equal(t, cfg, r.Config())
}
