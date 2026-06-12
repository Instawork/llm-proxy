package adminrollup

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewBackendErrors(t *testing.T) {
	_, err := newBackend(Config{Backend: "bogus"})
	require.Error(t, err)

	_, err = newBackend(Config{Backend: BackendRedis})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redis config is missing")
}

func TestNewBackendInfersMemoryWhenNoRedis(t *testing.T) {
	be, err := newBackend(Config{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, BackendMemory, be.kind())
}

func TestNewBackendInfersRedisFromAddress(t *testing.T) {
	// No live Redis required: invalid address fails at ping, not at selection.
	_, err := newBackend(Config{
		Backend: "",
		Redis:   &config.RedisConfig{Address: "127.0.0.1:1", DBSet: true},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "redis ping")
}
