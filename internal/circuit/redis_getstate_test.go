package circuit

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// GetState now fetches the state key and the half-open marker in one
// pipelined round-trip; every state the two keys can encode must still map
// to the same result as the former sequential GET-then-EXISTS.
func TestRedisStore_GetState_PipelinedReadMatchesKeySemantics(t *testing.T) {
	store, mr := newRedisStoreForBehaviorTest(t, Config{}.Defaults())
	ctx := context.Background()
	const key = "openai:gpt-4o"

	got, err := store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateClosed, got, "no keys at all")

	require.NoError(t, mr.Set(store.stateKey(key), "open"))
	require.NoError(t, mr.Set(store.halfOpenKey(key), "1"))
	got, err = store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateOpen, got, "state key wins while present")

	require.NoError(t, mr.Set(store.stateKey(key), "half_open"))
	got, err = store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateHalfOpen, got, "explicit half_open state")

	mr.Del(store.stateKey(key))
	got, err = store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateHalfOpen, got, "cooldown elapsed: marker alone means half-open")

	mr.Del(store.halfOpenKey(key))
	got, err = store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateClosed, got, "marker expired too: back to closed")

	require.NoError(t, mr.Set(store.stateKey(key), "garbage"))
	got, err = store.GetState(ctx, key)
	require.NoError(t, err)
	require.Equal(t, StateClosed, got, "unknown state value with no marker is closed")
}

// blackHoleRedis accepts connections and reads forever without ever
// answering — a Redis that is up at the TCP level but not serving.
func blackHoleRedis(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

// A stalled Redis must cost a request at most redisStateReadTimeout, not the
// caller's (streaming-length) deadline, and must fail open to Closed.
func TestRedisStore_GetState_StalledRedisFailsOpenWithinReadTimeout(t *testing.T) {
	store := &RedisStore{
		cfg: Config{}.Defaults(),
		// Same options NewRedisStore applies; ContextTimeoutEnabled is what
		// makes the per-call deadline reach the socket read at all.
		rdb:     redis.NewClient(&redis.Options{Addr: blackHoleRedis(t), MaxRetries: -1, ContextTimeoutEnabled: true}),
		log:     slog.Default(),
		stopAll: make(chan struct{}),
	}
	t.Cleanup(func() { _ = store.Close() })

	// The caller's context is the long one a streaming request carries.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	start := time.Now()
	got, err := store.GetState(ctx, "openai:gpt-4o")
	elapsed := time.Since(start)

	require.NoError(t, err, "GetState must fail open, never surface the store error")
	require.Equal(t, StateClosed, got)
	require.Less(t, elapsed, 5*redisStateReadTimeout,
		"GetState held the request for %v against a stalled Redis; bound is %v", elapsed, redisStateReadTimeout)
	require.NoError(t, ctx.Err(), "the caller's own context must be left intact")
}
