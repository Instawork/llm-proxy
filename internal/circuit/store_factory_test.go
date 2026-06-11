package circuit

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func TestFactory_MemoryDefault(t *testing.T) {
	st, err := Factory(Config{Backend: "memory", FailureThreshold: 5, WindowSeconds: 60, CooldownSeconds: 60})
	require.NoError(t, err)
	require.NotNil(t, st)
}

func TestFactory_RedisBackend(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := Config{
		Backend: "redis", FailureThreshold: 5, WindowSeconds: 60, CooldownSeconds: 60,
		RedisAddress: mr.Addr(),
	}
	st, err := Factory(cfg)
	require.NoError(t, err)
	require.NotNil(t, st)
}
