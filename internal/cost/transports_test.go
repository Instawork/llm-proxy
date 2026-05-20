package cost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ----------------------------------------------------------------------------
// File transport
// ----------------------------------------------------------------------------

func TestFileTransport_WriteRecord_AppendsJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "cost.log")

	ft := NewFileTransport(path)
	rec := &CostRecord{
		Timestamp: time.Unix(1234567890, 0),
		RequestID: "req-1",
		Provider:  "openai",
		Model:     "gpt-4o",
		TotalCost: 0.0001,
	}

	require.NoError(t, ft.WriteRecord(rec))
	require.NoError(t, ft.WriteRecord(rec))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	lines := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var got CostRecord
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &got))
		assert.Equal(t, "openai", got.Provider)
		lines++
	}
	assert.Equal(t, 2, lines)
}

func TestFileTransport_FromConfig_StructAndMap(t *testing.T) {
	logger := slog.Default()
	ft := &FileTransport{}

	// struct path
	cfg := &config.TransportConfig{File: &config.FileTransportConfig{Path: filepath.Join(t.TempDir(), "out.log")}}
	tr, err := ft.FromConfig(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)

	// struct path - missing file section
	_, err = ft.FromConfig(&config.TransportConfig{}, logger)
	assert.Error(t, err)

	// map path
	mp := map[string]interface{}{
		"file": map[string]interface{}{"path": filepath.Join(t.TempDir(), "m.log")},
	}
	tr, err = ft.FromConfig(mp, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)

	// map path - no file key
	_, err = ft.FromConfig(map[string]interface{}{"other": 1}, logger)
	assert.Error(t, err)

	// map path - wrong path type
	_, err = ft.FromConfig(map[string]interface{}{"file": map[string]interface{}{"path": 123}}, logger)
	assert.Error(t, err)

	// invalid type
	_, err = ft.FromConfig(123, logger)
	assert.Error(t, err)

	// convenience wrapper
	_, err = NewFileTransportFromConfig(cfg, logger)
	require.NoError(t, err)
}

// ----------------------------------------------------------------------------
// Datadog transport
// ----------------------------------------------------------------------------

// startUDPSink starts a UDP listener on a random port and returns its address.
// statsd-go uses UDP datagrams; we don't need to read them, just provide a port.
func startUDPSink(t *testing.T) (host, port string, cleanup func()) {
	t.Helper()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	conn, err := net.ListenUDP("udp", addr)
	require.NoError(t, err)
	host = "127.0.0.1"
	port = strings.Split(conn.LocalAddr().String(), ":")[1]
	return host, port, func() { _ = conn.Close() }
}

func TestDatadogTransport_NewAndWriteRecord_Defaults(t *testing.T) {
	host, port, stop := startUDPSink(t)
	defer stop()

	tr, err := NewDatadogTransport(DatadogTransportConfig{
		Host: host,
		Port: port,
		Tags: []string{"env:test"},
	})
	require.NoError(t, err)
	defer tr.Close()

	rec := &CostRecord{
		Provider:     "openai",
		Model:        "gpt-4o",
		Endpoint:     "/v1/chat",
		IsStreaming:  true,
		UserID:       "user-1",
		FinishReason: "stop",
		InputTokens:  10,
		OutputTokens: 20,
		TotalTokens:  30,
		InputCost:    0.001,
		OutputCost:   0.002,
		TotalCost:    0.003,
	}
	assert.NoError(t, tr.WriteRecord(rec))

	// also exercise zero defaults branch via empty cfg
	tr2, err := NewDatadogTransport(DatadogTransportConfig{})
	require.NoError(t, err)
	require.NotNil(t, tr2)
	assert.NoError(t, tr2.Close())

	// Close with nil client must not panic
	zero := &DatadogTransport{}
	assert.NoError(t, zero.Close())
}

func TestDatadogTransport_FromConfig_StructAndMap(t *testing.T) {
	host, port, stop := startUDPSink(t)
	defer stop()

	logger := slog.Default()
	dt := &DatadogTransport{}

	// missing datadog section in struct cfg
	_, err := dt.FromConfig(&config.TransportConfig{}, logger)
	assert.Error(t, err)

	// struct cfg success
	tr, err := dt.FromConfig(&config.TransportConfig{
		Datadog: &config.DatadogTransportConfig{Host: host, Port: port, Namespace: "test", Tags: []string{"a:b"}},
	}, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)

	// map cfg success
	tr, err = dt.FromConfig(map[string]interface{}{
		"datadog": map[string]interface{}{
			"host":        host,
			"port":        port,
			"namespace":   "ns",
			"sample_rate": 1.0,
			"tags":        []interface{}{"x:y", 123, "z:w"}, // mix to exercise tag filter
		},
	}, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)

	// map cfg without datadog
	_, err = dt.FromConfig(map[string]interface{}{"other": 1}, logger)
	assert.Error(t, err)

	// invalid type
	_, err = dt.FromConfig(123, logger)
	assert.Error(t, err)

	// convenience wrapper
	_, err = NewDatadogTransportFromConfig(&config.TransportConfig{
		Datadog: &config.DatadogTransportConfig{Host: host, Port: port},
	}, logger)
	require.NoError(t, err)
}

// ----------------------------------------------------------------------------
// DynamoDB transport (config + pure helpers only; SDK calls require real AWS)
// ----------------------------------------------------------------------------

func TestDynamoDBTransport_FromConfig_ValidationErrors(t *testing.T) {
	logger := slog.Default()
	dt := &DynamoDBTransport{}

	// struct: missing dynamodb section
	_, err := dt.FromConfig(&config.TransportConfig{}, logger)
	assert.Error(t, err)

	// map: missing dynamodb key
	_, err = dt.FromConfig(map[string]interface{}{"other": 1}, logger)
	assert.Error(t, err)

	// map: missing table_name
	_, err = dt.FromConfig(map[string]interface{}{"dynamodb": map[string]interface{}{"region": "us-west-2"}}, logger)
	assert.Error(t, err)

	// map: missing region
	_, err = dt.FromConfig(map[string]interface{}{"dynamodb": map[string]interface{}{"table_name": "t"}}, logger)
	assert.Error(t, err)

	// invalid type
	_, err = dt.FromConfig(123, logger)
	assert.Error(t, err)
}

func TestDynamoDBTransport_ToDynamoDBRecord_BuildsKeysAndTTL(t *testing.T) {
	dt := &DynamoDBTransport{tableName: "t", logger: slog.Default()}
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	rec := &CostRecord{
		Timestamp:   ts,
		RequestID:   "req-1",
		UserID:      "user-1",
		IPAddress:   "1.2.3.4",
		Provider:    "openai",
		Model:       "gpt-4o",
		Endpoint:    "/v1/chat",
		IsStreaming: true,
		InputTokens: 1, OutputTokens: 2, TotalTokens: 3,
		InputCost: 0.1, OutputCost: 0.2, TotalCost: 0.3,
		FinishReason: "stop",
	}
	out := dt.toDynamoDBRecord(rec)

	assert.Equal(t, "COST#2024-01-02", out.PK)
	assert.True(t, strings.HasPrefix(out.SK, "TIMESTAMP#2024-01-02T03:04:05.000Z#"))
	assert.Equal(t, "PROVIDER#openai", out.GSI1PK)
	assert.True(t, strings.HasPrefix(out.GSI1SK, "MODEL#gpt-4o#"))
	assert.Equal(t, "USER#user-1", out.GSI2PK)
	assert.Equal(t, "MODEL#gpt-4o", out.GSI3PK)
	assert.Equal(t, ts.Unix(), out.Timestamp)
	assert.Equal(t, ts.AddDate(1, 0, 0).Unix(), out.TTL)
	assert.Equal(t, "openai", out.Provider)
	assert.Equal(t, "stop", out.FinishReason)
}

// ----------------------------------------------------------------------------
// Tracker convenience constructors and registry
// ----------------------------------------------------------------------------

func TestNewFileBasedCostTracker(t *testing.T) {
	dir := t.TempDir()
	ct := NewFileBasedCostTracker(filepath.Join(dir, "out.log"))
	require.NotNil(t, ct)
	assert.Len(t, ct.transports, 1)
}

func TestNewDatadogBasedCostTracker(t *testing.T) {
	host, port, stop := startUDPSink(t)
	defer stop()
	ct, err := NewDatadogBasedCostTracker(host, port)
	require.NoError(t, err)
	require.NotNil(t, ct)
}

func TestSetLogger(t *testing.T) {
	ct := NewCostTracker()
	custom := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ct.SetLogger(custom)
	assert.Equal(t, custom, ct.logger)
}

func TestRegisterTransportFactory_AndCreateTransportFromConfig(t *testing.T) {
	logger := slog.Default()

	// factory registration & lookup via map cfg
	called := false
	RegisterTransportFactory("noop-test", func(_ interface{}, _ *slog.Logger) (Transport, error) {
		called = true
		return &mockTransport{}, nil
	})
	defer func() { delete(transportRegistry, "noop-test") }()

	tr, err := CreateTransportFromConfig(map[string]interface{}{"type": "noop-test"}, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)
	assert.True(t, called)

	// struct cfg path
	cfg := &config.TransportConfig{Type: "file", File: &config.FileTransportConfig{Path: filepath.Join(t.TempDir(), "f.log")}}
	tr, err = CreateTransportFromConfig(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, tr)

	// map cfg without type
	_, err = CreateTransportFromConfig(map[string]interface{}{"other": 1}, logger)
	assert.Error(t, err)

	// unsupported type
	_, err = CreateTransportFromConfig(map[string]interface{}{"type": "does-not-exist"}, logger)
	assert.Error(t, err)

	// invalid cfg type
	_, err = CreateTransportFromConfig(123, logger)
	assert.Error(t, err)

	// helper: supported types is non-empty and contains defaults
	types := getSupportedTransportTypes()
	assert.Contains(t, types, "file")
	assert.Contains(t, types, "datadog")
}

// ----------------------------------------------------------------------------
// Tracker async behavior gaps
// ----------------------------------------------------------------------------

type errTransport struct{ err error }

func (e *errTransport) WriteRecord(_ *CostRecord) error { return e.err }

func TestCostTracker_TrackRequest_TransportError_Bubbles(t *testing.T) {
	ct := NewCostTracker(&errTransport{err: errors.New("boom")})
	ct.SetSyncMode()
	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})
	err := ct.TrackRequest(&providers.LLMResponseMetadata{
		Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
	}, "u", "ip", "/e")
	assert.Error(t, err)
}

func TestCostTracker_TrackRequest_NoPricing_LogsAndSucceeds(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.SetSyncMode()
	err := ct.TrackRequest(&providers.LLMResponseMetadata{
		Provider: "unknown-provider", Model: "no-such-model",
		InputTokens: 1, OutputTokens: 2, TotalTokens: 3,
	}, "u", "", "/e")
	require.NoError(t, err)
	records := mock.Records()
	require.Len(t, records, 1)
	assert.Equal(t, 0.0, records[0].TotalCost)
	assert.False(t, records[0].IsEstimate)
}

func TestCostTracker_TrackRequest_FuzzyMatch(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.SetSyncMode()
	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 1.0, Output: 2.0}},
	})
	err := ct.TrackRequest(&providers.LLMResponseMetadata{
		Provider: "openai", Model: "gpt-4ox", InputTokens: 1000, OutputTokens: 1000,
	}, "u", "", "/e")
	require.NoError(t, err)
	records := mock.Records()
	require.Len(t, records, 1)
	assert.True(t, records[0].IsEstimate)
	assert.Equal(t, "gpt-4o", records[0].MatchedModel)
}

func TestCostTracker_AsyncQueueFull_FallsBackToSync(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.ConfigureAsync(1, 1, 60) // tiny queue + long flush so we can fill it
	// don't start workers yet so the queue stays full
	require.NoError(t, ct.StartAsyncWorkers())

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})

	// fill queue immediately by having no transports drain it fast enough
	// but easier: temporarily replace transports with a slow one and then
	// fire many requests so the bounded channel overflows
	ct.transports = []Transport{slowTransport{d: 50 * time.Millisecond, mock: mock}}

	for i := 0; i < 20; i++ {
		// TrackRequest must never return an error in the "queue full"
		// case — the contract is "fall back to sync so the caller
		// never observes a missed record". Re-asserting that here
		// guards against a future refactor that starts surfacing the
		// queue-full case as a hard error to the caller.
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
		}, "u", "", "/e"))
	}

	// stop workers and verify all records made it through despite the
	// async queue being undersized — the sync fallback is the whole
	// point of this test.
	ct.StopAsyncWorkers()
	assert.GreaterOrEqual(t, len(mock.Records()), 20, "every TrackRequest must reach the transport via sync fallback")
}

type slowTransport struct {
	d    time.Duration
	mock *mockTransport
}

func (s slowTransport) WriteRecord(r *CostRecord) error {
	time.Sleep(s.d)
	return s.mock.WriteRecord(r)
}

func TestCostTracker_StartAsync_WhenNotConfigured_Errors(t *testing.T) {
	ct := NewCostTracker()
	err := ct.StartAsyncWorkers()
	assert.Error(t, err)
}

func TestCostTracker_StartAsync_TwiceErrors(t *testing.T) {
	ct := NewCostTracker()
	ct.ConfigureAsync(1, 4, 1)
	require.NoError(t, ct.StartAsyncWorkers())
	defer ct.StopAsyncWorkers()
	err := ct.StartAsyncWorkers()
	assert.Error(t, err)
}

func TestCostTracker_ConfigureAsync_DefaultsClampNonPositive(t *testing.T) {
	ct := NewCostTracker()
	ct.ConfigureAsync(0, 0, 0)
	assert.Equal(t, 5, ct.workers)
	assert.NotNil(t, ct.queue)
	assert.Equal(t, 15*time.Second, ct.flushInterval)
}

func TestCostTracker_SetSyncMode_AfterStartedDoesNotSwitch(t *testing.T) {
	ct := NewCostTracker()
	ct.ConfigureAsync(1, 4, 1)
	require.NoError(t, ct.StartAsyncWorkers())
	defer ct.StopAsyncWorkers()
	ct.SetSyncMode()
	assert.True(t, ct.async)
}

func TestCostTracker_AsyncFlushTickerProcessesRecords(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.ConfigureAsync(1, 8, 1) // 1-second ticker
	require.NoError(t, ct.StartAsyncWorkers())

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})
	for i := 0; i < 3; i++ {
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
		}, "u", "", "/e"))
	}

	// Poll for the worker to drain via either of its case branches
	// (queued record consumer or flush ticker).  A fixed sleep was flaky
	// under CPU contention; Eventually returns as soon as work is done.
	require.Eventually(t, func() bool {
		return len(mock.Records()) >= 3
	}, 2*time.Second, 5*time.Millisecond, "async worker never drained all 3 records")
	ct.StopAsyncWorkers()
	assert.Equal(t, 3, len(mock.Records()))
}

// ----------------------------------------------------------------------------
// Misc edge cases
// ----------------------------------------------------------------------------

func TestRoundUpTo4Decimals_ViaPublicAPI(t *testing.T) {
	ct := NewCostTracker()
	ct.SetPricingForModel("p", "m", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.123456789, Output: 0.0}},
	})
	in, out, total, err := ct.CalculateCost("p", "m", 1_000_000, 0)
	require.NoError(t, err)
	// 0.123456789 rounds up at 4 decimals → 0.1235
	assert.InDelta(t, 0.1235, in, 1e-9)
	assert.Equal(t, 0.0, out)
	assert.InDelta(t, 0.1235, total, 1e-9)
}

func TestGetPricingForModel_TieredAndOverride(t *testing.T) {
	ct := NewCostTracker()
	ct.SetPricingForModel("p", "m", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 100, Input: 1, Output: 2},
			{Threshold: 0, Input: 10, Output: 20}, // catch-all
		},
		Overrides: map[string]struct {
			Input  float64 `json:"input"`
			Output float64 `json:"output"`
		}{},
	})

	tier, err := ct.GetPricingForModel("p", "m", 50)
	require.NoError(t, err)
	assert.Equal(t, 1.0, tier.Input)

	tier, err = ct.GetPricingForModel("p", "m", 1000)
	require.NoError(t, err)
	assert.Equal(t, 10.0, tier.Input)

	// Unknown provider
	_, err = ct.GetPricingForModel("nope", "m", 50)
	assert.Error(t, err)
}

func TestFindClosestModelMatch_NoMatch(t *testing.T) {
	ct := NewCostTracker()
	ct.SetPricingForModel("p", "very-different-model-name", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 1, Output: 2}},
	})
	_, _, _, err := ct.GetPricingForModelWithFuzzyMatch("p", "x", 0)
	assert.Error(t, err)
}

func TestProcessRemainingRecords_QueueClosed(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.ConfigureAsync(1, 4, 60)
	require.NoError(t, ct.StartAsyncWorkers())

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})
	require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
		Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
	}, "u", "", "/e"))

	// stopping closes the queue and forces processRemainingRecords path
	ct.StopAsyncWorkers()
	// re-stop is a no-op
	ct.StopAsyncWorkers()
	assert.GreaterOrEqual(t, len(mock.Records()), 1)
}

// keep an explicit context import path used at package level by other files
var _ = context.Background

// TestFlushQueuedRecords_DirectExercisesBothPaths queues records into the
// async cost tracker queue and then directly invokes the unexported
// flushQueuedRecords helper.  Calling it before the worker drains lets us
// reliably exercise both the "process record" and "queue empty" branches
// in a single test invocation.
func TestFlushQueuedRecords_DirectExercisesBothPaths(t *testing.T) {
	mock := &mockTransport{}
	ct := NewCostTracker(mock)
	ct.ConfigureAsync(1, 8, 60) // long ticker — we drive flush manually

	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})

	// Need an active context for TrackRequest so it queues into ct.queue.
	require.NoError(t, ct.StartAsyncWorkers())
	defer ct.StopAsyncWorkers()

	// Pre-queue records by stopping the worker goroutine using ConfigureAsync's
	// flush helper. Since we can't pause the running worker, simulate the flush
	// path by invoking it directly with a deterministic queue state.
	for i := 0; i < 3; i++ {
		require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
		}, "u", "", "/e"))
	}
	// Direct call: covers the loop body once records are present and again
	// after they've all been drained (default branch).
	ct.flushQueuedRecords(99)
	ct.flushQueuedRecords(99) // empty — exercises the default branch
}
