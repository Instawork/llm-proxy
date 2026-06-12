package adminrollup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTopNWithOther(t *testing.T) {
	t.Run("uncapped returns all sorted by value", func(t *testing.T) {
		top, other := topNWithOther(map[string]float64{
			"b": 10,
			"a": 20,
			"c": 5,
		}, 0)
		require.InDelta(t, 0, other, 0.001)
		require.Len(t, top, 3)
		require.Equal(t, "a", top[0].Name)
		require.InDelta(t, 20, top[0].Val, 0.001)
		require.Equal(t, "b", top[1].Name)
		require.Equal(t, "c", top[2].Name)
	})

	t.Run("caps with other bucket", func(t *testing.T) {
		top, other := topNWithOther(map[string]float64{
			"high":   100,
			"mid":    50,
			"low":    10,
			"tiny":   1,
			"smidge": 1,
		}, 2)
		require.Len(t, top, 2)
		require.Equal(t, "high", top[0].Name)
		require.Equal(t, "mid", top[1].Name)
		require.InDelta(t, 12, other, 0.001) // low + tiny + smidge
	})

	t.Run("tie breaks by name", func(t *testing.T) {
		top, other := topNWithOther(map[string]float64{
			"z-key": 5,
			"a-key": 5,
		}, 0)
		require.InDelta(t, 0, other, 0.001)
		require.Equal(t, "a-key", top[0].Name)
		require.Equal(t, "z-key", top[1].Name)
	})
}

func TestDimMemberFieldRoundTrip(t *testing.T) {
	key := DimMemberField("gpt-4", "tokens")
	member, field, ok := parseDimMemberField(key)
	require.True(t, ok)
	require.Equal(t, "gpt-4", member)
	require.Equal(t, "tokens", field)
}

func TestMergeTodayUsageAggregates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := "2026-06-12"
	caps := TopNCaps{ByKey: 2, ByUser: 2}

	p := NewPersister(store, MetricUsage)
	p.QueueDelta(day, Delta{
		Totals: map[string]float64{"requests": 10, "tokens": 1000},
		Dimensions: map[string]map[string]float64{
			"by_model": {
				DimMemberField("gpt-4", "requests"):  6,
				DimMemberField("gpt-4", "tokens"):    600,
				DimMemberField("claude", "requests"): 4,
				DimMemberField("claude", "tokens"):   400,
			},
			"by_key": {
				DimMemberField("key-a", "tokens"): 500,
				DimMemberField("key-b", "tokens"): 300,
				DimMemberField("key-c", "tokens"): 200,
			},
			"by_user": {
				DimMemberField("user-1", "tokens"): 700,
				DimMemberField("user-2", "tokens"): 200,
				DimMemberField("user-3", "tokens"): 100,
			},
		},
	})
	p.FlushNow()

	snap := map[string]interface{}{"available": true}
	store.MergeToday(ctx, MetricUsage, day, snap, caps)

	require.Equal(t, int64(10), snap["requests_today"])
	require.Equal(t, int64(1000), snap["tokens_today"])

	byModel, ok := snap["by_model"].(map[string]map[string]float64)
	require.True(t, ok)
	require.InDelta(t, 600, byModel["gpt-4"]["tokens"], 0.001)
	require.InDelta(t, 400, byModel["claude"]["tokens"], 0.001)

	byKey, ok := snap["by_key"].(map[string]map[string]float64)
	require.True(t, ok)
	require.InDelta(t, 500, byKey["key-a"]["tokens"], 0.001)
	require.InDelta(t, 300, byKey["key-b"]["tokens"], 0.001)
	require.InDelta(t, 200, byKey["other_key"]["tokens"], 0.001)
	require.NotContains(t, byKey, "key-c")

	byUser, ok := snap["by_user"].(map[string]map[string]float64)
	require.True(t, ok)
	require.InDelta(t, 700, byUser["user-1"]["tokens"], 0.001)
	require.InDelta(t, 200, byUser["user-2"]["tokens"], 0.001)
	require.InDelta(t, 100, byUser["other_user"]["tokens"], 0.001)
}

func TestMergeTodayPIIAggregates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := "2026-06-12"
	caps := TopNCaps{ByKey: 2}

	p := NewPersister(store, MetricPII)
	p.QueueDelta(day, Delta{
		Totals: map[string]float64{
			"requests_scanned":  100,
			"requests_with_pii": 25,
			"entities_total":    40,
			"fail_open":         5,
			"fail_closed":       3,
			"oversize":          2,
		},
		Dimensions: map[string]map[string]float64{
			"by_entity": {
				"EMAIL_ADDRESS": 20,
				"US_SSN":        15,
			},
			"by_provider": {
				"openai": 18,
				"gemini": 7,
			},
			"by_key": {
				"key-a": 30,
				"key-b": 20,
				"key-c": 10,
			},
		},
	})
	p.FlushNow()

	snap := map[string]interface{}{"available": true}
	store.MergeToday(ctx, MetricPII, day, snap, caps)

	require.Equal(t, int64(100), snap["requests_scanned"])
	require.Equal(t, int64(25), snap["requests_with_pii"])
	require.Equal(t, int64(40), snap["entities_total"])
	// clean = 100 - 5 - 3 - 2 = 90; rate = 25/90
	require.InDelta(t, 25.0/90.0, snap["detection_rate"].(float64), 0.001)

	byEntity, ok := snap["by_entity"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, byEntity, 2)
	require.Equal(t, "EMAIL_ADDRESS", byEntity[0]["name"])
	require.Equal(t, int64(20), byEntity[0]["count"])

	topKeys, ok := snap["top_keys"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, topKeys, 3) // top 2 + other_key bucket
	names := []string{
		topKeys[0]["name"].(string),
		topKeys[1]["name"].(string),
		topKeys[2]["name"].(string),
	}
	require.Contains(t, names, "key-a")
	require.Contains(t, names, "key-b")
	require.Contains(t, names, "other_key")
}

func TestMemoryBackendApplyDeltaMergeToday(t *testing.T) {
	store, err := NewStore(Config{
		Enabled:       true,
		Backend:       BackendMemory,
		RetentionDays: 7,
		HistoryDays:   3,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	day := "2026-06-12"
	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, Delta{
		Totals: map[string]float64{"spend_usd": 4.2, "requests": 9},
	}))

	snap := map[string]interface{}{}
	store.MergeToday(ctx, MetricCost, day, snap, TopNCaps{ByKey: 100})
	require.InDelta(t, 4.2, snap["spend_today_usd"].(float64), 0.001)
	require.Equal(t, int64(9), snap["requests_today"])
}

func TestArchiveDailyFromAggregates(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	caps := TopNCaps{ByKey: 100}

	p := NewPersister(store, MetricUsage)
	p.QueueDelta(day, Delta{
		Totals: map[string]float64{"requests": 5, "tokens": 500},
	})
	p.FlushNow()

	require.NoError(t, store.ArchiveDailyFromAggregates(ctx, MetricUsage, day, caps))

	history, err := store.LoadHistory(ctx, MetricUsage)
	require.NoError(t, err)
	var archived map[string]interface{}
	for _, row := range history {
		if row.Day == day {
			archived = row.Data
			break
		}
	}
	require.NotNil(t, archived)
	require.InDelta(t, 5, archived["requests_today"], 0.001)
	require.InDelta(t, 500, archived["tokens_today"], 0.001)

	totals, err := store.loadHash(ctx, totalsKey(MetricUsage, day))
	require.NoError(t, err)
	require.Empty(t, totals, "today hash keys should be removed after archive")
}

func TestMergeTodayCostWithByKey(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")

	p := NewPersister(store, MetricCost)
	p.QueueDelta(day, Delta{
		Totals: map[string]float64{
			"spend_usd": 2.0,
			"requests":  1,
		},
		Dimensions: map[string]map[string]float64{
			"by_key": {
				DimMemberField("iw:abc", "spend_usd"): 2.0,
				DimMemberField("iw:abc", "requests"):  1,
			},
			"by_provider": {
				DimMemberField("openai", "spend_usd"): 2.0,
				DimMemberField("openai", "requests"):  1,
			},
		},
	})
	p.FlushNow()

	snap := map[string]interface{}{}
	store.MergeToday(ctx, MetricCost, day, snap, TopNCaps{ByKey: 100})
	byKey, ok := snap["by_key"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, byKey, 1)
	require.Equal(t, "iw:abc", byKey[0]["key_id"])
}

func TestParseDimMemberFieldInvalid(t *testing.T) {
	_, _, ok := parseDimMemberField("no-separator")
	require.False(t, ok)
}

func TestFlattenScopeDimSkipsNonTokenFields(t *testing.T) {
	out := flattenScopeDim(map[string]float64{
		"scope|requests": 5,
		"scope|tokens":   99,
		"plain":          1,
	})
	require.Len(t, out, 1)
	require.InDelta(t, 99, out["scope"], 0.001)
}

func TestUsageMergeTodayIncludesByProvider(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")

	p := NewPersister(store, MetricUsage)
	p.QueueDelta(day, Delta{
		Totals: map[string]float64{"requests": 1, "tokens": 10},
		Dimensions: map[string]map[string]float64{
			"by_provider": {
				DimMemberField("anthropic", "tokens"): 10,
			},
		},
	})
	p.FlushNow()

	snap := map[string]interface{}{}
	store.MergeToday(ctx, MetricUsage, day, snap, TopNCaps{})
	byProv, ok := snap["by_provider"].(map[string]map[string]float64)
	require.True(t, ok)
	require.InDelta(t, 10, byProv["anthropic"]["tokens"], 0.001)
}

func TestTryElectArchiverMemoryBackend(t *testing.T) {
	store, err := NewStore(Config{
		Enabled:       true,
		Backend:       BackendMemory,
		RetentionDays: 7,
		HistoryDays:   3,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.True(t, store.TryElectArchiver(ctx, MetricCost, "2026-06-12", "instance-a"))
	require.False(t, store.TryElectArchiver(ctx, MetricCost, "2026-06-12", "instance-b"))
}
