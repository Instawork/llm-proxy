package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

const testMonth = "2026-11"

func agentSpendRollup(t *testing.T) (*adminrollup.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Backend: adminrollup.BackendRedis,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, mr
}

// agentSpendCostTable writes cost rows with mixed providers on both sides of
// the November 2026 boundary into a fake DynamoDB cost table.
func agentSpendCostTable(t *testing.T) *cost.DynamoDBTransport {
	t.Helper()
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	tp, err := cost.NewDynamoDBTransport(cost.DynamoDBTransportConfig{TableName: "cost", Region: "us-west-2"})
	require.NoError(t, err)

	nov := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec := nov.AddDate(0, 1, 0)
	rows := []struct {
		user, provider string
		ts             time.Time
		usd            float64
	}{
		{"Support Bot", "openai", nov, 1.00},
		{"Support Bot", "anthropic", dec.Add(-time.Second), 2.50},
		{"Support Bot", "gemini", dec, 50},
		{"Support Bot", "openai", nov.Add(-time.Second), 50},
		{"Old Support", "bedrock", nov.Add(48 * time.Hour), 0.50},
		{"Helper", "anthropic", nov.Add(time.Hour), 7.00},
		{"finch-agent-12", "anthropic", nov.Add(time.Hour), 99},
	}
	for i, r := range rows {
		require.NoError(t, tp.WriteRecord(&cost.CostRecord{
			RequestID: string(rune('a' + i)), UserID: r.user, Provider: r.provider, Model: "m", Timestamp: r.ts, TotalCost: r.usd,
		}))
	}
	return tp
}

const testMapping = `id,slug,label-as-sent
12,support-bot,Support%20Bot
12,support-bot,Old%20Support
3,helper,Helper
9,helper-2,Helper
9,helper-2,Helper
20,quiet,Quiet%20Agent
`

func seedOpts(dryRun bool) seedOptions {
	return seedOptions{Month: testMonth, Prefix: defaultAgentSpendPrefix, DryRun: dryRun, Now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
}

func userSpend(t *testing.T, store *adminrollup.Store, label string) float64 {
	t.Helper()
	v, err := store.UserMonthlySpendUSD(context.Background(), adminrollup.MetricCost, testMonth, label)
	require.NoError(t, err)
	return v
}

func TestAgentSpendSeed(t *testing.T) {
	ctx := context.Background()
	costs := agentSpendCostTable(t)
	rollup, _ := agentSpendRollup(t)
	mappings, err := parseAgentMapping(strings.NewReader(testMapping))
	require.NoError(t, err)

	// Live spend already counted under the new label is added to, not replaced.
	require.NoError(t, rollup.ApplyMonthlyUserSpend(ctx, adminrollup.MetricCost, testMonth, "finch-agent-12", 1.00))

	var out bytes.Buffer
	res, err := executeAgentSpendSeed(ctx, &out, costs, rollup, mappings, seedOpts(true))
	require.NoError(t, err)
	assert.Zero(t, res.Failed)
	assert.InDelta(t, 1.00, userSpend(t, rollup, "finch-agent-12"), 1e-9, "dry run writes nothing")
	assert.Contains(t, out.String(), "DRY RUN")
	assert.Contains(t, out.String(), "would seed $4.00")
	assert.Contains(t, out.String(), "Nothing was written")

	out.Reset()
	res, err = executeAgentSpendSeed(ctx, &out, costs, rollup, mappings, seedOpts(false))
	require.NoError(t, err)
	assert.Zero(t, res.Failed)
	text := out.String()
	assert.InDelta(t, 5.00, userSpend(t, rollup, "finch-agent-12"), 1e-9, "3.50 + 0.50 seeded on top of 1.00 live")
	assert.Zero(t, userSpend(t, rollup, "finch-agent-3"), "ambiguous label is not attributed")
	assert.Zero(t, userSpend(t, rollup, "finch-agent-9"), "ambiguous label is not attributed")
	assert.Zero(t, userSpend(t, rollup, "finch-agent-20"))

	assert.Contains(t, text, "seeded $4.00")
	assert.Contains(t, text, "AMBIGUOUS, not seeded")
	assert.Contains(t, text, "finch-agent-3 (helper), finch-agent-9 (helper-2)")
	assert.Contains(t, text, "$7.00")
	assert.Contains(t, text, "finch-agent-12 <- Old Support, Support Bot")
	assert.Contains(t, text, "UNSEEDED AGENTS: every old label of these agents is ambiguous: finch-agent-3, finch-agent-9")
	assert.Contains(t, text, string(seedNoSpend))

	out.Reset()
	res, err = executeAgentSpendSeed(ctx, &out, costs, rollup, mappings, seedOpts(false))
	require.NoError(t, err)
	assert.Zero(t, res.Failed)
	assert.InDelta(t, 5.00, userSpend(t, rollup, "finch-agent-12"), 1e-9, "second run is a no-op")
	assert.Equal(t, 2, strings.Count(out.String(), string(seedAlreadyDone)))
	assert.Contains(t, out.String(), "seeded $0.00; 2 already seeded")

	out.Reset()
	_, err = executeAgentSpendSeed(ctx, &out, costs, rollup, mappings, seedOpts(true))
	require.NoError(t, err)
	assert.NotContains(t, out.String(), string(seedWouldWrite)+"\n")
	assert.Contains(t, out.String(), "would seed $0.00; 2 already seeded")
}

func TestAgentSpendSeed_NewMappingRowAfterFirstRun(t *testing.T) {
	ctx := context.Background()
	costs := agentSpendCostTable(t)
	rollup, _ := agentSpendRollup(t)

	first, err := parseAgentMapping(strings.NewReader("id,slug,label-as-sent\n12,support-bot,Support%20Bot\n"))
	require.NoError(t, err)
	_, err = executeAgentSpendSeed(ctx, &bytes.Buffer{}, costs, rollup, first, seedOpts(false))
	require.NoError(t, err)
	assert.InDelta(t, 3.50, userSpend(t, rollup, "finch-agent-12"), 1e-9)

	second, err := parseAgentMapping(strings.NewReader("id,slug,label-as-sent\n12,support-bot,Support%20Bot\n12,support-bot,Old%20Support\n"))
	require.NoError(t, err)
	_, err = executeAgentSpendSeed(ctx, &bytes.Buffer{}, costs, rollup, second, seedOpts(false))
	require.NoError(t, err)
	assert.InDelta(t, 4.00, userSpend(t, rollup, "finch-agent-12"), 1e-9, "only the new label is added")
}

func TestAgentSpendSeed_RedisDownReportsFailures(t *testing.T) {
	costs := agentSpendCostTable(t)
	rollup, mr := agentSpendRollup(t)
	mappings, err := parseAgentMapping(strings.NewReader(testMapping))
	require.NoError(t, err)
	mr.Close()

	var out bytes.Buffer
	res, err := executeAgentSpendSeed(context.Background(), &out, costs, rollup, mappings, seedOpts(false))
	require.NoError(t, err)
	assert.Equal(t, 2, res.Failed)
	assert.Contains(t, out.String(), "FAILED: claim marker")
	assert.Contains(t, out.String(), "2 failed")
}

func TestAgentSpendReport(t *testing.T) {
	ctx := context.Background()
	rollup, _ := agentSpendRollup(t)
	for label, usd := range map[string]float64{
		"finch-agent-1":  250.10,
		"finch-agent-2":  100.00,
		"finch-agent-30": 199.99,
		"finch-agent-4":  5,
		"finch-agent-":   500,
		"Support Bot":    900,
	} {
		require.NoError(t, rollup.ApplyMonthlyUserSpend(ctx, adminrollup.MetricCost, testMonth, label, usd))
	}

	var out bytes.Buffer
	require.NoError(t, executeAgentSpendReport(ctx, &out, rollup, testMonth, defaultAgentSpendPrefix, defaultAgentSoftLimitCents, defaultAgentHardLimitCents))
	text := out.String()
	assert.Contains(t, text, "soft=$100.00 hard=$200.00")
	assert.NotContains(t, text, "Support Bot", "only governed labels are listed")
	assert.NotContains(t, text, "$500.00", "the bare prefix is not a governed label")

	lines := strings.Split(text, "\n")
	var body []string
	for _, l := range lines {
		if strings.HasPrefix(l, "finch-agent-") {
			body = append(body, strings.Join(strings.Fields(l), " "))
		}
	}
	assert.Equal(t, []string{
		"finch-agent-1 $250.10 OVER HARD",
		"finch-agent-30 $199.99 OVER SOFT",
		"finch-agent-2 $100.00 OVER SOFT",
		"finch-agent-4 $5.00 ok",
	}, body)
	assert.Contains(t, text, "4 agent label(s); 1 at or over hard; 2 at or over soft but under hard")
}

func TestParseAgentMapping(t *testing.T) {
	got, err := parseAgentMapping(strings.NewReader("\ufeffID, Slug ,label-as-sent\n7,a,\"x,y\"\n7,a,\"x,y\"\n8,b,z\n"))
	require.NoError(t, err)
	assert.Equal(t, []agentMapping{{"7", "a", "x,y"}, {"8", "b", "z"}}, got)

	bad := map[string]string{
		"header":       "id,name,label\n1,a,b\n",
		"no rows":      "id,slug,label-as-sent\n",
		"non-numeric":  "id,slug,label-as-sent\nabc,a,b\n",
		"zero":         "id,slug,label-as-sent\n0,a,b\n",
		"leading zero": "id,slug,label-as-sent\n07,a,b\n",
		"empty label":  "id,slug,label-as-sent\n1,a,\n",
		"two slugs":    "id,slug,label-as-sent\n1,a,b\n1,c,d\n",
		"columns":      "id,slug,label-as-sent\n1,a\n",
	}
	for name, in := range bad {
		_, err := parseAgentMapping(strings.NewReader(in))
		assert.Error(t, err, name)
	}
}

func TestBuildSeedSources(t *testing.T) {
	sources, err := buildSeedSources([]agentMapping{
		{"12", "s", "Support%20Bot"},
		{"12", "s", "Support Bot"},
		{"5", "p", "100%25%20Agent"},
	}, defaultAgentSpendPrefix)
	require.NoError(t, err)
	require.Len(t, sources, 2)
	assert.Equal(t, "100% Agent", sources[0].Label)
	assert.Equal(t, "Support Bot", sources[1].Label, "sent forms that decode alike are one source")
	assert.Equal(t, []string{"Support Bot", "Support%20Bot"}, sources[1].SentLabels)
	assert.False(t, sources[1].ambiguous())

	_, err = buildSeedSources([]agentMapping{{"1", "s", "finch-agent-1"}}, defaultAgentSpendPrefix)
	assert.Error(t, err, "a label already under the prefix would be double counted")
	_, err = buildSeedSources([]agentMapping{{"1", "s", "bad%zz"}}, defaultAgentSpendPrefix)
	assert.Error(t, err)
}

func TestAgentSeedNeedsProdGuard(t *testing.T) {
	assert.True(t, agentSeedNeedsProdGuard("production", false))
	assert.False(t, agentSeedNeedsProdGuard("production", true))
	assert.False(t, agentSeedNeedsProdGuard("staging", false))
}

func TestValidateAgentSpendMonth(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	assert.NoError(t, validateAgentSpendMonth("2026-11", now))
	assert.NoError(t, validateAgentSpendMonth("2026-10", now))
	for _, bad := range []string{"", "2026-12", "2026-1", "2026/11", "2026-11-01"} {
		assert.Error(t, validateAgentSpendMonth(bad, now), bad)
	}
}

func TestValidateAgentSpendPrefix(t *testing.T) {
	assert.NoError(t, validateAgentSpendPrefix("finch-agent-"))
	for _, bad := range []string{"", "a|b", "a/b", "a b"} {
		assert.Error(t, validateAgentSpendPrefix(bad), bad)
	}
}
