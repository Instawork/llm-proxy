package cost

import (
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
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
		for _, p := range []string{"anthropic", "gemini"} {
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

	t.Run("anthropic prices cache tokens from configured rates", func(t *testing.T) {
		ct, mock := newTracker()
		require.NoError(t, ct.TrackRequest(meta("anthropic"), "u", "127.0.0.1", "/v1/messages", ""))
		rec := mock.Records()[0]
		assert.InDelta(t, 3+0.3+3.75, rec.InputCost, 1e-9)
		assert.InDelta(t, 15, rec.OutputCost, 1e-9)
		assert.InDelta(t, 3+0.3+3.75+15, rec.TotalCost, 1e-9)
	})

	t.Run("providers with inclusive cached counts are not surcharged", func(t *testing.T) {
		ct, mock := newTracker()
		require.NoError(t, ct.TrackRequest(meta("gemini"), "u", "127.0.0.1", "/v1/interactions", ""))
		rec := mock.Records()[0]
		assert.InDelta(t, 3, rec.InputCost, 1e-9)
		assert.InDelta(t, 18, rec.TotalCost, 1e-9)
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

type recordingStats struct {
	mu    sync.Mutex
	calls []struct {
		provider, keyID, userID, model          string
		spendUSD, inputSpendUSD, outputSpendUSD float64
		inputTokens, outputTokens               int
	}
}

func (r *recordingStats) RecordRequest(
	provider, keyID, userID, model string,
	spendUSD, inputSpendUSD, outputSpendUSD float64,
	inputTokens, outputTokens int,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, struct {
		provider, keyID, userID, model          string
		spendUSD, inputSpendUSD, outputSpendUSD float64
		inputTokens, outputTokens               int
	}{provider, keyID, userID, model, spendUSD, inputSpendUSD, outputSpendUSD, inputTokens, outputTokens})
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
