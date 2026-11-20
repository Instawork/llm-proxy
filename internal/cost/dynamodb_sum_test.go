package cost

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

func TestDynamoDBTransport_SumUserCost_MixedProvidersAcrossMonthBoundary(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	tp, err := NewDynamoDBTransport(DynamoDBTransportConfig{TableName: "cost", Region: "us-west-2"})
	require.NoError(t, err)

	write := func(id, user, provider string, ts time.Time, usd float64) {
		t.Helper()
		require.NoError(t, tp.WriteRecord(&CostRecord{
			RequestID: id, UserID: user, Provider: provider, Model: "m", Timestamp: ts, TotalCost: usd,
		}))
	}
	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec := nov.AddDate(0, 1, 0)
	write("in-1", "Support Bot", "openai", nov, 1.00)
	write("in-2", "Support Bot", "anthropic", nov.Add(10*24*time.Hour), 2.50)
	write("in-3", "Support Bot", "bedrock", dec.Add(-time.Second), 0.25)
	write("before", "Support Bot", "gemini", nov.Add(-time.Second), 100)
	write("after", "Support Bot", "openai", dec, 100)
	write("other", "Other Bot", "openai", nov.Add(time.Hour), 100)

	got, err := tp.SumUserCost(context.Background(), "Support Bot", nov, dec)
	require.NoError(t, err)
	assert.Equal(t, 3, got.Rows)
	assert.InDelta(t, 3.75, got.TotalUSD, 1e-9)

	got, err = tp.SumUserCost(context.Background(), "Nobody", nov, dec)
	require.NoError(t, err)
	assert.Equal(t, UserCostTotal{}, got)

	_, err = tp.SumUserCost(context.Background(), "", nov, dec)
	assert.Error(t, err)
	_, err = tp.SumUserCost(context.Background(), "Support Bot", dec, nov)
	assert.Error(t, err)
}
