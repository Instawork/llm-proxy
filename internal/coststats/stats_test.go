package coststats

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
)

func TestRecorderKeySpendUSD(t *testing.T) {
	r := NewRecorder()
	if got := r.KeySpendUSD(context.Background(), "missing"); got != 0 {
		t.Fatalf("missing key spend = %v", got)
	}
	r.RecordRequest("openai", "iw:abc", "", "m", 0.25, 0, 0, 1, 1)
	if got := r.KeySpendUSD(context.Background(), "iw:abc"); got != 0.25 {
		t.Fatalf("key spend = %v want 0.25", got)
	}
}

// A cost-limited key's blocked requests are never tracked, so maybeRollDay may
// not fire after midnight UTC. KeySpendUSD must treat a stale-day bucket as zero
// so the key isn't blocked forever into the new day.
func TestRecorderKeySpendUSD_StaleDayReturnsZero(t *testing.T) {
	r := NewRecorder()
	r.RecordRequest("openai", "iw:abc", "", "m", 5.00, 0, 0, 1, 1)
	if got := r.KeySpendUSD(context.Background(), "iw:abc"); got != 5.00 {
		t.Fatalf("same-day key spend = %v want 5.00", got)
	}

	// Simulate the recorder's bucket being stuck on a previous UTC day.
	r.mu.Lock()
	r.dayKey = "2000-01-01"
	r.mu.Unlock()

	if got := r.KeySpendUSD(context.Background(), "iw:abc"); got != 0 {
		t.Fatalf("stale-day key spend = %v want 0", got)
	}
}

// When bound to a shared rollup store, KeySpendUSD must return the fleet-wide
// spend so cost limits are enforced cluster-wide. We simulate another instance
// by writing a by_key delta straight to the store, then assert this recorder
// (which has zero local spend for that key) still sees it.
func TestRecorderKeySpendUSD_FleetWideFromStore(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))

	day := time.Now().UTC().Format("2006-01-02")
	// Another instance recorded $4.00 for this key directly in Redis.
	err = store.ApplyDelta(context.Background(), adminrollup.MetricCost, day, adminrollup.Delta{
		Totals: map[string]float64{"spend_usd": 4.00},
		Dimensions: map[string]map[string]float64{
			"by_key": {adminrollup.DimMemberField("iw:fleet", "spend_usd"): 4.00},
		},
	})
	if err != nil {
		t.Fatalf("ApplyDelta: %v", err)
	}

	// This recorder has no local spend for the key, but must see the fleet total.
	if got := r.KeySpendUSD(context.Background(), "iw:fleet"); got != 4.00 {
		t.Fatalf("fleet-wide key spend = %v want 4.00", got)
	}

	// max(fleet, local): a larger local (not yet flushed to Redis) wins so this
	// instance never under-counts its own just-recorded spend.
	r.RecordRequest("openai", "iw:fleet", "", "m", 9.00, 0, 0, 1, 1)
	if got := r.KeySpendUSD(context.Background(), "iw:fleet"); got != 9.00 {
		t.Fatalf("max(fleet,local) = %v want 9.00", got)
	}
}

func TestRecorderAggregatesByKeyAndProvider(t *testing.T) {
	r := NewRecorder()

	r.RecordRequest("openai", "iw:abcdefgh999", "", "gpt-4o-mini", 0.0012, 0.0008, 0.0004, 100, 50)
	r.RecordRequest("openai", "iw:abcdefgh999", "", "gpt-4o-mini", 0.0008, 0.0005, 0.0003, 80, 40)
	r.RecordRequest("anthropic", "iw:zzzzzzzz0001", "", "claude-haiku", 0.002, 0.0015, 0.0005, 200, 100)

	snap := r.Snapshot()

	if got := snap["spend_today_usd"].(float64); got < 0.0039 || got > 0.0041 {
		t.Fatalf("spend_today_usd = %v, want ~0.004", got)
	}
	if got := snap["requests_today"].(int64); got != 3 {
		t.Fatalf("requests_today = %d, want 3", got)
	}

	byKeyRaw := snap["by_key"]
	var byKey []keySpend
	switch rows := byKeyRaw.(type) {
	case []keySpend:
		byKey = rows
	case []map[string]interface{}:
		byKey = make([]keySpend, len(rows))
		for i, row := range rows {
			byKey[i] = keySpend{
				KeyID:          row["key_id"].(string),
				SpendUSD:       snapFloat(row["spend_usd"]),
				InputSpendUSD:  snapFloat(row["input_spend_usd"]),
				OutputSpendUSD: snapFloat(row["output_spend_usd"]),
				Requests:       int64(snapFloat(row["requests"])),
			}
		}
	default:
		t.Fatalf("by_key type = %T", byKeyRaw)
	}
	if len(byKey) != 2 {
		t.Fatalf("by_key len = %d, want 2", len(byKey))
	}
	if byKey[0].KeyID != "iw:abcdefgh999" || byKey[0].Requests != 2 {
		t.Fatalf("top by_key = %+v", byKey[0])
	}

	byProvider := snap["by_provider"].([]providerSpend)
	if len(byProvider) != 2 {
		t.Fatalf("by_provider len = %d, want 2", len(byProvider))
	}
	if byProvider[0].Name != "openai" && byProvider[1].Name != "openai" {
		t.Fatalf("by_provider missing openai = %+v", byProvider)
	}

	recent := snap["recent"].([]recentEntry)
	if len(recent) != 3 || recent[0].Provider != "anthropic" {
		t.Fatalf("recent newest = %+v", recent[0])
	}
}

func TestRecorderRollsDayBucket(t *testing.T) {
	r := NewRecorder()
	r.dayKey = time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	r.spendTodayUSD = 9.99
	r.requestsToday = 5

	r.RecordRequest("openai", "iw:abc", "", "gpt-4o-mini", 0.01, 0.006, 0.004, 10, 5)

	snap := r.Snapshot()
	if got := snap["spend_today_usd"].(float64); got != 0.01 {
		t.Fatalf("spend after roll = %v, want 0.01", got)
	}
	if got := snap["requests_today"].(int64); got != 1 {
		t.Fatalf("requests after roll = %d, want 1", got)
	}
}

func TestRecorderSnapshotStaleDayWithoutTraffic(t *testing.T) {
	r := NewRecorder()
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	r.dayKey = yesterday
	r.spendTodayUSD = 9.99
	r.requestsToday = 42

	snap := r.Snapshot()
	if got, _ := snap["day"].(string); got != today {
		t.Fatalf("day = %q want %q", got, today)
	}
	if got := snap["spend_today_usd"].(float64); got != 0 {
		t.Fatalf("spend_today_usd = %v want 0 for stale bucket", got)
	}
	if got := snap["requests_today"].(int64); got != 0 {
		t.Fatalf("requests_today = %d want 0 for stale bucket", got)
	}
}

func TestRecorderSnapshotPreservesLocalByKeyBeforeRedisFlush(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
	r.RecordRequest("gemini", "iw:abcdefgh999", "", "gemini-3-flash-preview", 0.0013, 0.0009, 0.0004, 100, 50)

	snap := r.Snapshot()
	rows := byKeyRowsFromSnap(snap["by_key"])
	if len(rows) != 1 {
		t.Fatalf("by_key len = %d, want 1", len(rows))
	}
	if got := snapFloat(rows[0]["spend_usd"]); got != 0.0013 {
		t.Fatalf("by_key spend = %v want 0.0013", got)
	}
}

func TestRecorderNilSafe(t *testing.T) {
	var r *Recorder
	r.RecordRequest("openai", "", "", "", 1, 0.6, 0.4, 1, 1)
	if got := r.Snapshot()["available"].(bool); got {
		t.Fatal("nil recorder should report unavailable")
	}
}
