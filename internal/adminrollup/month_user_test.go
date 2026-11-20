package adminrollup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func memoryTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(Config{Enabled: true, Backend: BackendMemory})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func forEachBackend(t *testing.T, fn func(t *testing.T, store *Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { fn(t, memoryTestStore(t)) })
	t.Run("redis", func(t *testing.T) { fn(t, testStore(t)) })
}

func TestUserMonthlySpend_ApplyReadAndList(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()
		const month = "2026-11"

		v, err := store.UserMonthlySpendUSD(ctx, MetricCost, month, "finch-agent-1")
		require.NoError(t, err)
		require.Equal(t, 0.0, v)

		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, "finch-agent-1", 1.25))
		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, "finch-agent-1", 1.25))
		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, "finch-agent-2", 0.5))
		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, "finch-agent-3", 0))

		v, err = store.UserMonthlySpendUSD(ctx, MetricCost, month, "finch-agent-1")
		require.NoError(t, err)
		require.InDelta(t, 2.5, v, 1e-9)

		all, err := store.UserMonthlySpendAll(ctx, MetricCost, month)
		require.NoError(t, err)
		require.Len(t, all, 2, "zero-spend writes must not create a field")
		require.InDelta(t, 2.5, all["finch-agent-1"], 1e-9)
		require.InDelta(t, 0.5, all["finch-agent-2"], 1e-9)
	})
}

func TestUserMonthlySpend_SeparateFromKeyHash(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()
		const month = "2026-11"
		const id = "finch-agent-1"

		require.NoError(t, store.ApplyMonthlyKeySpend(ctx, MetricCost, month, id, 9))

		v, err := store.UserMonthlySpendUSD(ctx, MetricCost, month, id)
		require.NoError(t, err)
		require.Equal(t, 0.0, v)
		all, err := store.UserMonthlySpendAll(ctx, MetricCost, month)
		require.NoError(t, err)
		require.Empty(t, all)

		allowed, err := store.ReserveUserMonthlySpend(ctx, MetricCost, month, id, 0.1, 100)
		require.NoError(t, err)
		require.True(t, allowed, "per-key spend must not count against the per-user cap")
	})
}

func TestUserMonthlySpend_MonthRollover(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()
		const id = "finch-agent-1"
		const limitCents = 100

		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, "2026-10", id, 5))
		allowed, err := store.ReserveUserMonthlySpend(ctx, MetricCost, "2026-10", id, 0.1, limitCents)
		require.NoError(t, err)
		require.False(t, allowed, "October is over its cap")

		v, err := store.UserMonthlySpendUSD(ctx, MetricCost, "2026-11", id)
		require.NoError(t, err)
		require.Equal(t, 0.0, v, "a new month starts at 0")
		all, err := store.UserMonthlySpendAll(ctx, MetricCost, "2026-11")
		require.NoError(t, err)
		require.Empty(t, all)

		allowed, err = store.ReserveUserMonthlySpend(ctx, MetricCost, "2026-11", id, 0.1, limitCents)
		require.NoError(t, err)
		require.True(t, allowed, "November is independent of October")

		reserved, err := store.ReservedUserMonthlySpendUSD(ctx, MetricCost, "2026-10", id)
		require.NoError(t, err)
		require.Equal(t, 0.0, reserved, "the November reservation must not land in October")
	})
}

func TestReserveUserMonthlySpend(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()
		const month = "2026-11"
		const id = "finch-agent-7"
		const limitCents = 100

		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, id, 0.40))

		allowed, err := store.ReserveUserMonthlySpend(ctx, MetricCost, month, id, 0.65, limitCents)
		require.NoError(t, err)
		require.True(t, allowed, "recorded $0.40 is under the $1.00 cap")

		allowed, err = store.ReserveUserMonthlySpend(ctx, MetricCost, month, id, 0.10, limitCents)
		require.NoError(t, err)
		require.False(t, allowed, "recorded + reserved $1.05 has reached the cap")

		reserved, err := store.ReservedUserMonthlySpendUSD(ctx, MetricCost, month, id)
		require.NoError(t, err)
		require.InDelta(t, 0.65, reserved, 1e-9, "a blocked reservation adds nothing")

		allowed, err = store.ReserveUserMonthlySpend(ctx, MetricCost, month, "finch-agent-8", 0.10, limitCents)
		require.NoError(t, err)
		require.True(t, allowed, "labels are capped independently")

		require.NoError(t, store.AddUserMonthlyReservation(ctx, MetricCost, month, id, -0.65))
		allowed, err = store.ReserveUserMonthlySpend(ctx, MetricCost, month, id, 0.10, limitCents)
		require.NoError(t, err)
		require.True(t, allowed, "releasing the reservation restores headroom")
	})
}

func TestAddUserMonthlyReservation_FloorsAtZero(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()
		const month = "2026-11"
		const id = "finch-agent-1"

		require.NoError(t, store.AddUserMonthlyReservation(ctx, MetricCost, month, id, 0.30))
		require.NoError(t, store.AddUserMonthlyReservation(ctx, MetricCost, month, id, -10))

		reserved, err := store.ReservedUserMonthlySpendUSD(ctx, MetricCost, month, id)
		require.NoError(t, err)
		require.Equal(t, 0.0, reserved, "over-release must floor at 0")

		// A negative reservation would create headroom above the cap.
		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, id, 1.0))
		allowed, err := store.ReserveUserMonthlySpend(ctx, MetricCost, month, id, 0.01, 100)
		require.NoError(t, err)
		require.False(t, allowed)
	})
}

func TestUserMonthlyKeys_RedisNamesAndTTL(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	const month = "2026-11"

	require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, month, "finch-agent-1", 1))
	_, err := store.ReserveUserMonthlySpend(ctx, MetricCost, month, "finch-agent-1", 0.5, 1000)
	require.NoError(t, err)

	rdb := store.be.(*redisBackend).rdb
	spendKey := "llm:admin:cost:month:2026-11:by_user"
	reservedKey := "llm:admin:cost:month:2026-11:month_reserved_user"
	require.Equal(t, spendKey, monthUserKey(MetricCost, month))
	require.Equal(t, reservedKey, monthReservedUserKey(MetricCost, month))

	field, err := rdb.HGet(ctx, spendKey, "finch-agent-1|spend_usd").Float64()
	require.NoError(t, err)
	require.Equal(t, 1.0, field)
	field, err = rdb.HGet(ctx, reservedKey, "finch-agent-1").Float64()
	require.NoError(t, err)
	require.Equal(t, 0.5, field)

	for _, k := range []string{spendKey, reservedKey} {
		ttl, err := rdb.TTL(ctx, k).Result()
		require.NoError(t, err)
		require.Equal(t, monthTTL, ttl, k)
	}
}

func TestUserMonthlySpend_NilAndEmpty(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	allowed, err := nilStore.ReserveUserMonthlySpend(ctx, MetricCost, "2026-11", "finch-agent-1", 1, 1)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, nilStore.AddUserMonthlyReservation(ctx, MetricCost, "2026-11", "finch-agent-1", 1))
	require.NoError(t, nilStore.ApplyMonthlyUserSpend(ctx, MetricCost, "2026-11", "finch-agent-1", 1))
	v, err := nilStore.UserMonthlySpendUSD(ctx, MetricCost, "2026-11", "finch-agent-1")
	require.NoError(t, err)
	require.Equal(t, 0.0, v)
	all, err := nilStore.UserMonthlySpendAll(ctx, MetricCost, "2026-11")
	require.NoError(t, err)
	require.Nil(t, all)

	store := memoryTestStore(t)
	require.NoError(t, store.ApplyMonthlyUserSpend(ctx, MetricCost, "2026-11", "", 1))
	all, err = store.UserMonthlySpendAll(ctx, MetricCost, "2026-11")
	require.NoError(t, err)
	require.Empty(t, all, "an empty label is never written")
}
