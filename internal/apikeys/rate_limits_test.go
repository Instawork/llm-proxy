package apikeys

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRateLimitOverrides(t *testing.T) {
	t.Run("nil record", func(t *testing.T) {
		_, ok := RateLimitOverrides(nil)
		assert.False(t, ok)
	})

	t.Run("all zero", func(t *testing.T) {
		_, ok := RateLimitOverrides(&APIKey{})
		assert.False(t, ok)
	})

	t.Run("rpm set", func(t *testing.T) {
		lc, ok := RateLimitOverrides(&APIKey{RateLimitRPM: 42})
		assert.True(t, ok)
		assert.Equal(t, 42, lc.RequestsPerMinute)
	})

	t.Run("all windows", func(t *testing.T) {
		lc, ok := RateLimitOverrides(&APIKey{
			RateLimitRPM: 1,
			RateLimitTPM: 2,
			RateLimitRPD: 3,
			RateLimitTPD: 4,
		})
		assert.True(t, ok)
		assert.Equal(t, 1, lc.RequestsPerMinute)
		assert.Equal(t, 2, lc.TokensPerMinute)
		assert.Equal(t, 3, lc.RequestsPerDay)
		assert.Equal(t, 4, lc.TokensPerDay)
	})
}
