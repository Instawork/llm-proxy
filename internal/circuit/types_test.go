package circuit

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfig_Validate_AllBranches(t *testing.T) {
	good := Config{
		Enabled: true, Backend: "memory", Mode: "log",
		FailureThreshold: 5, WindowSeconds: 60, CooldownSeconds: 60,
	}

	t.Run("disabled short-circuits", func(t *testing.T) {
		c := good
		c.Enabled = false
		c.Backend = "weird"
		assert.NoError(t, c.Validate())
	})

	t.Run("invalid backend", func(t *testing.T) {
		c := good
		c.Backend = "weird"
		assert.Error(t, c.Validate())
	})

	t.Run("invalid mode", func(t *testing.T) {
		c := good
		c.Mode = "weird"
		assert.Error(t, c.Validate())
	})

	t.Run("invalid retry contribution mode", func(t *testing.T) {
		c := good
		c.RetryContributionMode = "weird"
		assert.Error(t, c.Validate())
	})

	t.Run("oversize degraded signal", func(t *testing.T) {
		c := good
		c.DegradedSignal = strings.Repeat("x", MaxDegradedSignalLength+1)
		assert.Error(t, c.Validate())
	})

	t.Run("negative per-provider rollup threshold", func(t *testing.T) {
		c := good
		c.PerProviderRollupThreshold = -1
		assert.Error(t, c.Validate())
	})

	t.Run("negative per-provider rollup window", func(t *testing.T) {
		c := good
		c.PerProviderRollupWindowSeconds = -1
		assert.Error(t, c.Validate())
	})

	t.Run("ok", func(t *testing.T) {
		assert.NoError(t, good.Validate())
	})
}
