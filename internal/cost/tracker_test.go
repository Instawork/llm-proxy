package cost

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTransport is a thread-safe in-memory transport used by tests.
// The mutex is required because async tracker tests append records
// from the worker goroutine while the test goroutine reads back via
// require.Eventually / Records().
type mockTransport struct {
	mu      sync.Mutex
	records []CostRecord
}

func (m *mockTransport) WriteRecord(record *CostRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, *record)
	return nil
}

func (m *mockTransport) Close() error {
	return nil
}

// Records returns a snapshot of all records written so far.  Callers
// must use this (or len(Records())) rather than touching m.records
// directly so the lock is always taken.
func (m *mockTransport) Records() []CostRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CostRecord, len(m.records))
	copy(out, m.records)
	return out
}

func TestCostTracker_TrackRequest_Sync(t *testing.T) {
	ct := NewCostTracker()
	ct.SetSyncMode() // synchronous

	mock := &mockTransport{}
	ct.AddTransport(mock)

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.01, Output: 0.02},
		},
	})

	meta := &providers.LLMResponseMetadata{
		Provider:     "openai",
		Model:        "gpt-4o",
		InputTokens:  100,
		OutputTokens: 50,
	}

	err := ct.TrackRequest(meta, "user1", "127.0.0.1", "/v1/chat", "iw:0123456789ab…deadbeef")
	assert.NoError(t, err)

	records := mock.Records()
	assert.Len(t, records, 1)
	assert.Equal(t, "openai", records[0].Provider)
	assert.Equal(t, "gpt-4o", records[0].Model)
	assert.Equal(t, "user1", records[0].UserID)
	assert.Equal(t, "iw:0123456789ab…deadbeef", records[0].KeyID)
	assert.Equal(t, 100, records[0].InputTokens)
	assert.Equal(t, 50, records[0].OutputTokens)
	// cost = 100 * 0.01/1000 + 50 * 0.02/1000 = 0.001 + 0.001 = 0.0001
	assert.InDelta(t, 0.0001, records[0].TotalCost, 0.0001)
}

func TestCostTracker_TrackRequest_CacheTokens(t *testing.T) {
	ct := NewCostTracker()
	ct.SetSyncMode()

	mock := &mockTransport{}
	ct.AddTransport(mock)

	ct.SetPricingForModel("anthropic", "claude-sonnet", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 3, Output: 15}},
	})

	meta := &providers.LLMResponseMetadata{
		Provider:                 "anthropic",
		Model:                    "claude-sonnet",
		InputTokens:              12,
		OutputTokens:             40,
		CacheReadInputTokens:     48000,
		CacheCreationInputTokens: 2500,
	}

	require.NoError(t, ct.TrackRequest(meta, "user1", "127.0.0.1", "/v1/messages", ""))

	records := mock.Records()
	require.Len(t, records, 1)
	assert.Equal(t, 12, records[0].InputTokens)
	assert.Equal(t, 48000, records[0].CacheReadInputTokens)
	assert.Equal(t, 2500, records[0].CacheCreationInputTokens)
}

func TestCostTracker_TrackRequest_CacheTokensInInputCost(t *testing.T) {
	newTracker := func() (*CostTracker, *mockTransport) {
		ct := NewCostTracker()
		ct.SetSyncMode()
		mock := &mockTransport{}
		ct.AddTransport(mock)
		for _, p := range []string{"anthropic", "bedrock", "bedrock-mantle", "gemini", "openai"} {
			ct.SetPricingForModel(p, "m", &ModelPricing{
				Tiers: []PricingTier{{Threshold: 0, Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
			})
		}
		return ct, mock
	}
	meta := func(provider string) *providers.LLMResponseMetadata {
		return &providers.LLMResponseMetadata{
			Provider:                 provider,
			Model:                    "m",
			InputTokens:              1_000_000,
			OutputTokens:             1_000_000,
			CacheReadInputTokens:     1_000_000,
			CacheCreationInputTokens: 1_000_000,
		}
	}

	for _, provider := range []string{"anthropic", "bedrock", "bedrock-mantle", "gemini", "openai"} {
		t.Run(provider+" prices cache tokens from configured rates", func(t *testing.T) {
			ct, mock := newTracker()
			require.NoError(t, ct.TrackRequest(meta(provider), "u", "127.0.0.1", "/v1/messages", ""))
			rec := mock.Records()[0]
			assert.InDelta(t, 3+0.3+3.75, rec.InputCost, 1e-9)
			assert.InDelta(t, 15, rec.OutputCost, 1e-9)
			assert.InDelta(t, 3+0.3+3.75+15, rec.TotalCost, 1e-9)
		})
	}

	t.Run("unset cache prices fall back to the input rate", func(t *testing.T) {
		ct, mock := newTracker()
		ct.SetPricingForModel("anthropic", "nocache", &ModelPricing{
			Tiers: []PricingTier{{Threshold: 0, Input: 2, Output: 10}},
		})
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider:                 "anthropic",
			Model:                    "nocache",
			CacheReadInputTokens:     1_000_000,
			CacheCreationInputTokens: 1_000_000,
		}, "u", "127.0.0.1", "/v1/messages", ""))
		assert.InDelta(t, 4, mock.Records()[0].InputCost, 1e-9)
	})

	t.Run("override keeps its own cache prices", func(t *testing.T) {
		ct, mock := newTracker()
		ct.SetPricingForModel("anthropic", "us.m", &ModelPricing{
			Tiers: []PricingTier{{Threshold: 0, Input: 3.3, Output: 16.5, CacheRead: 0.33, CacheWrite: 4.125}},
			Overrides: map[string]PricingOverride{
				"global.m": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
			},
		})
		ct.SetPricingForModel("anthropic", "global.m", ct.pricingConfig["anthropic"]["us.m"])
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider:                 "anthropic",
			Model:                    "global.m",
			InputTokens:              1_000_000,
			CacheReadInputTokens:     1_000_000,
			CacheCreationInputTokens: 1_000_000,
		}, "u", "127.0.0.1", "/v1/messages", ""))
		rec := mock.Records()[0]
		assert.InDelta(t, 3+0.3+3.75, rec.InputCost, 1e-9)
	})

	t.Run("tier is selected from the full prompt including cache tokens", func(t *testing.T) {
		ct, mock := newTracker()
		ct.SetPricingForModel("anthropic", "tiered", &ModelPricing{
			Tiers: []PricingTier{
				{Threshold: 100_000, Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
				{Threshold: 0, Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
			},
		})
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider:             "anthropic",
			Model:                "tiered",
			InputTokens:          1_000,
			CacheReadInputTokens: 200_000,
		}, "u", "127.0.0.1", "/v1/messages", ""))
		rec := mock.Records()[0]
		assert.InDelta(t, 0.01+0.2, rec.InputCost, 1e-9)
	})
}

func TestCostTracker_TrackRequest_Async(t *testing.T) {
	ct := NewCostTracker()
	ct.ConfigureAsync(1, 10, 1)

	mock := &mockTransport{}
	ct.AddTransport(mock)

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.01, Output: 0.02},
		},
	})

	require.NoError(t, ct.StartAsyncWorkers())
	defer ct.StopAsyncWorkers()

	meta := &providers.LLMResponseMetadata{
		Provider:     "openai",
		Model:        "gpt-4o",
		InputTokens:  100,
		OutputTokens: 50,
	}

	err := ct.TrackRequest(meta, "user1", "127.0.0.1", "/v1/chat", "")
	assert.NoError(t, err)

	// Poll for the async worker to drain the queue rather than sleeping
	// a fixed window — under CPU contention 50ms is sometimes not enough
	// and the test races on the records slice without a lock.
	require.Eventually(t, func() bool {
		return len(mock.Records()) >= 1
	}, 2*time.Second, 5*time.Millisecond, "async worker never drained the cost record")

	records := mock.Records()
	assert.Len(t, records, 1)
	assert.Equal(t, "openai", records[0].Provider)
	assert.InDelta(t, 0.0001, records[0].TotalCost, 0.0001)
}

type recordedStatsCall struct {
	provider, keyID, userID, model          string
	spendUSD, inputSpendUSD, outputSpendUSD float64
	inputTokens, outputTokens               int
	cacheReadTokens, cacheCreationTokens    int
}

type recordingStats struct {
	mu    sync.Mutex
	calls []recordedStatsCall
}

func (r *recordingStats) RecordRequestWithCache(
	provider, keyID, userID, model string,
	spendUSD, inputSpendUSD, outputSpendUSD float64,
	inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens int,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedStatsCall{
		provider, keyID, userID, model,
		spendUSD, inputSpendUSD, outputSpendUSD,
		inputTokens, outputTokens,
		cacheReadTokens, cacheCreationTokens,
	})
}

func TestCostTracker_StatsRecorder(t *testing.T) {
	stats := &recordingStats{}
	ct := NewCostTracker()
	ct.SetSyncMode()
	ct.SetStatsRecorder(stats)

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})

	meta := &providers.LLMResponseMetadata{
		Provider:     "openai",
		Model:        "gpt-4o",
		InputTokens:  100,
		OutputTokens: 50,
	}

	require.NoError(t, ct.TrackRequest(meta, "user1", "127.0.0.1", "/v1/chat", "iw:abcdefgh999"))
	stats.mu.Lock()
	defer stats.mu.Unlock()
	require.Len(t, stats.calls, 1)
	assert.Equal(t, "iw:abcdefgh999", stats.calls[0].keyID)
	assert.Equal(t, "openai", stats.calls[0].provider)
	assert.InDelta(t, 0.0001, stats.calls[0].spendUSD, 0.0001)
}

type recordingUsage struct {
	mu          sync.Mutex
	inputTokens []int
	cacheTokens [][2]int
}

func (r *recordingUsage) RecordRequestWithCache(_, _, _, _ string, inputTokens, _, cacheReadTokens, cacheCreationTokens int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputTokens = append(r.inputTokens, inputTokens)
	r.cacheTokens = append(r.cacheTokens, [2]int{cacheReadTokens, cacheCreationTokens})
}

func TestCostTracker_AdminRollupsIncludeCacheTokens(t *testing.T) {
	cases := []struct {
		provider        string
		wantRollupInput int
	}{
		{"anthropic", 12 + 30 + 5},
		{"bedrock", 12 + 30 + 5},
		{"bedrock-mantle", 12 + 30 + 5},
		{"gemini", 12 + 30 + 5},
		{"openai", 12 + 30 + 5},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			stats := &recordingStats{}
			usage := &recordingUsage{}
			mock := &mockTransport{}
			ct := NewCostTracker()
			ct.SetSyncMode()
			ct.AddTransport(mock)
			ct.SetStatsRecorder(stats)
			ct.SetUsageStatsRecorder(usage)

			require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
				Provider:                 tc.provider,
				Model:                    "m",
				InputTokens:              12,
				OutputTokens:             7,
				CacheReadInputTokens:     30,
				CacheCreationInputTokens: 5,
			}, "u", "127.0.0.1", "/v1/messages", ""))

			assert.Equal(t, 12, mock.Records()[0].InputTokens, "cost record keeps the cache-excluded count")
			stats.mu.Lock()
			defer stats.mu.Unlock()
			require.Len(t, stats.calls, 1)
			assert.Equal(t, tc.wantRollupInput, stats.calls[0].inputTokens)
			assert.Equal(t, 7, stats.calls[0].outputTokens)
			assert.Equal(t, 30, stats.calls[0].cacheReadTokens)
			assert.Equal(t, 5, stats.calls[0].cacheCreationTokens)
			usage.mu.Lock()
			defer usage.mu.Unlock()
			assert.Equal(t, []int{tc.wantRollupInput}, usage.inputTokens)
			assert.Equal(t, [][2]int{{30, 5}}, usage.cacheTokens)
		})
	}
}

func TestCalculateCost(t *testing.T) {
	ct := NewCostTracker()
	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.01, Output: 0.02},
		},
	})

	_, _, total, err := ct.CalculateCost("openai", "gpt-4o", 100, 50)
	assert.NoError(t, err)
	assert.InDelta(t, 0.0001, total, 0.0001)

	// Test missing model
	_, _, _, err = ct.CalculateCost("openai", "unknown", 100, 50)
	assert.Error(t, err)
}

// The cost-limit reservation is reconciled with CalculateCostWithCache, so it
// must equal what TrackRequest records, including cache writes priced above
// the input rate (gpt-5.6-sol: input 5.00, cache_write 6.25).
func TestCalculateCostWithCache_MatchesTrackedCost(t *testing.T) {
	ct := NewCostTracker()
	ct.SetSyncMode()
	mock := &mockTransport{}
	ct.AddTransport(mock)
	ct.SetPricingForModel("openai", "gpt-5.6-sol", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 272_000, Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
			{Threshold: 0, Input: 10, Output: 45, CacheRead: 1, CacheWrite: 12.5},
		},
	})

	for _, tc := range []struct{ in, read, write, out int }{
		{0, 0, 200_000, 0},
		{0, 0, 1_000_000, 0},
		{100_000, 400_000, 300_000, 5_000},
		{10_000, 0, 1_000, 500},
	} {
		_, _, got, err := ct.CalculateCostWithCache("openai", "gpt-5.6-sol", tc.in, tc.read, tc.write, tc.out)
		require.NoError(t, err)

		before := len(mock.Records())
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-5.6-sol",
			InputTokens: tc.in, CacheReadInputTokens: tc.read, CacheCreationInputTokens: tc.write, OutputTokens: tc.out,
		}, "u", "ip", "/e", "k"))
		recs := mock.Records()
		require.Len(t, recs, before+1)
		assert.Equal(t, recs[before].TotalCost, got, "%+v", tc)
	}

	// Cache-write tokens must bill at the write rate, not the input rate
	// (200k tokens stays in the short tier: 0.2 * 6.25 = 1.25, not 1.00).
	_, _, writes, err := ct.CalculateCostWithCache("openai", "gpt-5.6-sol", 0, 0, 200_000, 0)
	require.NoError(t, err)
	assert.InDelta(t, 1.25, writes, 1e-9)
}

func TestCostTracker_TrackRequest_BedrockInvokeStreamCacheMetrics(t *testing.T) {
	encodeChunk := func(t *testing.T, buf *bytes.Buffer, native any) {
		t.Helper()
		raw, err := json.Marshal(native)
		require.NoError(t, err)
		payload, err := json.Marshal(map[string]any{"bytes": raw})
		require.NoError(t, err)
		msg := eventstream.Message{
			Headers: eventstream.Headers{
				{Name: ":message-type", Value: eventstream.StringValue("event")},
				{Name: ":event-type", Value: eventstream.StringValue("chunk")},
			},
			Payload: payload,
		}
		require.NoError(t, eventstream.NewEncoder().Encode(buf, msg))
	}

	// Load actual configs/base.yml so pricing stays in sync with production config.
	cfg, err := config.LoadAndMergeConfigs([]string{"../../configs/base.yml"})
	require.NoError(t, err)

	loadBedrockModel := func(ct *CostTracker, modelName string) {
		t.Helper()
		mc, ok := cfg.Providers["bedrock"].Models[modelName]
		require.True(t, ok, "missing bedrock/%s in configs/base.yml", modelName)
		src, ok := mc.Pricing.(*config.ModelPricing)
		require.True(t, ok)
		mp := &ModelPricing{Tiers: make([]PricingTier, len(src.Tiers))}
		for i, tier := range src.Tiers {
			mp.Tiers[i] = PricingTier{
				Threshold:  tier.Threshold,
				Input:      tier.Input,
				Output:     tier.Output,
				CacheRead:  tier.CacheRead,
				CacheWrite: tier.CacheWrite,
			}
		}
		ct.SetPricingForModel("bedrock", modelName, mp)
	}

	ct := NewCostTracker()
	ct.SetSyncMode()
	mock := &mockTransport{}
	ct.AddTransport(mock)
	loadBedrockModel(ct, "us.anthropic.claude-haiku-5-5")
	loadBedrockModel(ct, "us.openai.gpt-6.1-terra")

	bp := providers.NewBedrockProxy()

	t.Run("short tier meters Anthropic message_stop cache read and write distinctly", func(t *testing.T) {
		var buf bytes.Buffer
		encodeChunk(t, &buf, map[string]any{
			"type": "message_stop",
			"amazon-bedrock-invocationMetrics": map[string]int{
				"inputTokenCount":           12_500,
				"outputTokenCount":          12_500,
				"cacheReadInputTokenCount":  50_000,
				"cacheWriteInputTokenCount": 25_000,
			},
		})

		md, err := bp.ParseResponseMetadata(&buf, true)
		require.NoError(t, err)
		md.Model = "us.anthropic.claude-haiku-5-5"

		before := len(mock.Records())
		require.NoError(t, ct.TrackRequest(md, "u", "127.0.0.1", "/bedrock/model/us.anthropic.claude-haiku-5-5/invoke-with-response-stream", ""))
		recs := mock.Records()
		require.Len(t, recs, before+1)
		rec := recs[before]

		assert.Equal(t, 12_500, rec.InputTokens)
		assert.Equal(t, 12_500, rec.OutputTokens)
		assert.Equal(t, 50_000, rec.CacheReadInputTokens)
		assert.Equal(t, 25_000, rec.CacheCreationInputTokens)
		assert.Equal(t, 100_000, rec.TotalTokens)

		// Prompt = 87.5k <= 100k (short tier: 0.11 / 0.55 / 0.011 / 0.1375):
		// raw input: 0.0125*0.11 + 0.05*0.011 + 0.025*0.1375 = 0.0053625 -> 0.0054
		// raw output: 0.0125*0.55 = 0.006875 -> 0.0069
		// raw total: 0.0122375 -> 0.0123
		assert.InDelta(t, 0.0054, rec.InputCost, 1e-9)
		assert.InDelta(t, 0.0069, rec.OutputCost, 1e-9)
		assert.InDelta(t, 0.0123, rec.TotalCost, 1e-9)

		inCost, outCost, totalCost, err := ct.CalculateCostWithCache(
			md.Provider, md.Model, md.InputTokens, md.CacheReadInputTokens, md.CacheCreationInputTokens, md.OutputTokens,
		)
		require.NoError(t, err)
		assert.Equal(t, rec.InputCost, inCost)
		assert.Equal(t, rec.OutputCost, outCost)
		assert.Equal(t, rec.TotalCost, totalCost)
	})

	t.Run("long tier selected when Anthropic message_stop cache tokens push prompt over 100K", func(t *testing.T) {
		var buf bytes.Buffer
		encodeChunk(t, &buf, map[string]any{
			"type": "message_stop",
			"amazon-bedrock-invocationMetrics": map[string]int{
				"inputTokenCount":           12_500,
				"outputTokenCount":          12_500,
				"cacheReadInputTokenCount":  75_000,
				"cacheWriteInputTokenCount": 25_000,
			},
		})

		md, err := bp.ParseResponseMetadata(&buf, true)
		require.NoError(t, err)
		md.Model = "us.anthropic.claude-haiku-5-5"

		before := len(mock.Records())
		require.NoError(t, ct.TrackRequest(md, "u", "127.0.0.1", "/bedrock/model/us.anthropic.claude-haiku-5-5/invoke-with-response-stream", ""))
		recs := mock.Records()
		require.Len(t, recs, before+1)
		rec := recs[before]

		// Prompt = 112.5k > 100k (long tier: 0.55 / 2.75 / 0.055 / 0.6875):
		// raw input: 0.0125*0.55 + 0.075*0.055 + 0.025*0.6875 = 0.0281875 -> 0.0282
		// raw output: 0.0125*2.75 = 0.034375 -> 0.0344
		// raw total: 0.0625625 -> 0.0626
		assert.InDelta(t, 0.0282, rec.InputCost, 1e-9)
		assert.InDelta(t, 0.0344, rec.OutputCost, 1e-9)
		assert.InDelta(t, 0.0626, rec.TotalCost, 1e-9)

		inCost, outCost, totalCost, err := ct.CalculateCostWithCache(
			md.Provider, md.Model, md.InputTokens, md.CacheReadInputTokens, md.CacheCreationInputTokens, md.OutputTokens,
		)
		require.NoError(t, err)
		assert.Equal(t, rec.InputCost, inCost)
		assert.Equal(t, rec.OutputCost, outCost)
		assert.Equal(t, rec.TotalCost, totalCost)
	})

	t.Run("non-Anthropic OpenAI invoke stream does not double-bill or escalate tier from invocationMetrics cache fields", func(t *testing.T) {
		var buf bytes.Buffer
		encodeChunk(t, &buf, map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop"}},
			"amazon-bedrock-invocationMetrics": map[string]int{
				"inputTokenCount":           125_000,
				"outputTokenCount":          31_250,
				"cacheReadInputTokenCount":  150_000,
				"cacheWriteInputTokenCount": 20_000,
			},
		})

		md, err := bp.ParseResponseMetadata(&buf, true)
		require.NoError(t, err)
		md.Model = "us.openai.gpt-6.1-terra"

		before := len(mock.Records())
		require.NoError(t, ct.TrackRequest(md, "u", "127.0.0.1", "/bedrock/model/us.openai.gpt-6.1-terra/invoke-with-response-stream", ""))
		recs := mock.Records()
		require.Len(t, recs, before+1)
		rec := recs[before]

		assert.Equal(t, 125_000, rec.InputTokens)
		assert.Equal(t, 31_250, rec.OutputTokens)
		assert.Zero(t, rec.CacheReadInputTokens)
		assert.Zero(t, rec.CacheCreationInputTokens)
		assert.Equal(t, 156_250, rec.TotalTokens)

		// Prompt stays at 125k <= 272k (short tier: 1.10 / 5.50), not escalated to >272k:
		// raw input: 0.125*1.10 = 0.1375
		// raw output: 0.03125*5.50 = 0.171875 -> 0.1719
		// raw total: 0.309375 -> 0.3094
		assert.InDelta(t, 0.1375, rec.InputCost, 1e-9)
		assert.InDelta(t, 0.1719, rec.OutputCost, 1e-9)
		assert.InDelta(t, 0.3094, rec.TotalCost, 1e-9)
	})
}
