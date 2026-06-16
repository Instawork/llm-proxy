package provision

import (
	"context"
	"errors"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicPool_ProvisionEmpty(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	p := NewAnthropicPool(rdb, "llm:provision:anthropic:available", "")

	_, err = p.Provision(context.Background(), "test")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEmptyPool))
}

func TestAnthropicPool_ProvisionAndStatus(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	listKey := "llm:provision:anthropic:available"
	mr.Lpush(listKey, "sk-ant-api03-test")

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	p := NewAnthropicPool(rdb, listKey, "")

	res, err := p.Provision(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-test", res.ActualKey)
	assert.Equal(t, UpstreamKindAnthropicPooled, res.UpstreamKind)

	n, ok := p.PoolStatus(context.Background())
	assert.True(t, ok)
	assert.Equal(t, 0, n)
}

func TestPoolAddAndLen(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	listKey := "llm:provision:anthropic:available"

	require.NoError(t, PoolAdd(ctx, rdb, listKey, "sk-ant-one"))
	require.NoError(t, PoolAdd(ctx, rdb, listKey, "sk-ant-two"))

	n, err := PoolLen(ctx, rdb, listKey)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}
