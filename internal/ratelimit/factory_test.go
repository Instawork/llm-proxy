package ratelimit

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactory_NilCfg_ReturnsNil(t *testing.T) {
	rl, err := Factory(nil)
	require.NoError(t, err)
	assert.Nil(t, rl)
}

func TestFactory_DisabledFeature_ReturnsNil(t *testing.T) {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = false
	rl, err := Factory(cfg)
	require.NoError(t, err)
	assert.Nil(t, rl)
}

func TestFactory_MemoryBackend(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Backend = "memory"
	rl, err := Factory(cfg)
	require.NoError(t, err)
	assert.NotNil(t, rl)
}

func TestFactory_DefaultBackendIsMemory(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Backend = ""
	rl, err := Factory(cfg)
	require.NoError(t, err)
	assert.NotNil(t, rl)
}

func TestFactory_RedisBackend(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := baseCfg()
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}
	rl, err := Factory(cfg)
	require.NoError(t, err)
	assert.NotNil(t, rl)
}

func TestFactory_UnknownBackend_ReturnsNil(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Backend = "unknown"
	rl, err := Factory(cfg)
	require.NoError(t, err)
	assert.Nil(t, rl)
}
