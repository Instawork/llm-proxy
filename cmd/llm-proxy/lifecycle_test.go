package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestWaitGroupWithTimeout(t *testing.T) {
	t.Run("returns true once the group drains", func(t *testing.T) {
		var wg sync.WaitGroup
		release := make(chan struct{})
		wg.Go(func() { <-release })
		go func() {
			time.Sleep(20 * time.Millisecond)
			close(release)
		}()
		assert.True(t, waitGroupWithTimeout(&wg, 5*time.Second))
	})

	t.Run("returns false when workers outlive the timeout", func(t *testing.T) {
		var wg sync.WaitGroup
		release := make(chan struct{})
		wg.Go(func() { <-release })
		t.Cleanup(func() { close(release) })
		start := time.Now()
		assert.False(t, waitGroupWithTimeout(&wg, 30*time.Millisecond))
		assert.Less(t, time.Since(start), 2*time.Second, "must not block past the timeout")
	})

	t.Run("empty group returns immediately", func(t *testing.T) {
		var wg sync.WaitGroup
		assert.True(t, waitGroupWithTimeout(&wg, time.Millisecond))
	})
}

// hangingCircuitStore blocks every read until the caller's context expires,
// simulating a partitioned Redis behind the circuit breaker.
type hangingCircuitStore struct {
	calls int
}

func (s *hangingCircuitStore) GetState(ctx context.Context, _ string) (circuit.State, error) {
	<-ctx.Done()
	return circuit.StateClosed, ctx.Err()
}

func (s *hangingCircuitStore) RecordTerminalFailure(ctx context.Context, _ string) (circuit.State, bool, error) {
	<-ctx.Done()
	return circuit.StateClosed, false, ctx.Err()
}

func (s *hangingCircuitStore) RecordSuccess(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *hangingCircuitStore) RecordProbeFailed(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *hangingCircuitStore) ForceOpen(ctx context.Context, _ string, _ int) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *hangingCircuitStore) GetStats(ctx context.Context, _ string) (*circuit.ProviderStats, error) {
	s.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHealthHandler_StoreTimeoutFailsOpen(t *testing.T) {
	prevStore, prevPM, prevCfg, prevTimeout := globalCircuitStore, globalProviderManager, globalCircuitConfig, healthStoreTimeout
	t.Cleanup(func() {
		globalCircuitStore, globalProviderManager, globalCircuitConfig, healthStoreTimeout = prevStore, prevPM, prevCfg, prevTimeout
	})

	store := &hangingCircuitStore{}
	globalCircuitStore = store
	globalProviderManager = providers.NewProviderManager()
	globalCircuitConfig = circuit.Config{Backend: "redis", Mode: "per_model"}
	healthStoreTimeout = 50 * time.Millisecond

	rec := httptest.NewRecorder()
	start := time.Now()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	elapsed := time.Since(start)

	// One shared deadline for the whole handler: the probe returns in roughly
	// one timeout even though every provider read hangs.
	assert.Less(t, elapsed, time.Second, "health must not stall on a hung circuit store")
	assert.Equal(t, len(circuitBreakerProviders), store.calls, "each provider is still attempted")

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Status         string `json:"status"`
		CircuitBreaker struct {
			Providers map[string]map[string]any `json:"providers"`
		} `json:"circuit_breaker"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body.Status, "liveness stays healthy; the store outage is reported, not fatal")
	for _, name := range circuitBreakerProviders {
		assert.Equal(t, "stats_unavailable", body.CircuitBreaker.Providers[name]["error"], name)
	}
}
