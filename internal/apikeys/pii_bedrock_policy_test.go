package apikeys

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanBypassPIIOffNonBedrockPolicy(t *testing.T) {
	// Restore the (empty) built-in default after mutating the shared allowlist.
	t.Cleanup(func() { SetPIIOffNonBedrockBypassAdmins(nil) })

	// Ships empty: no admin can bypass until explicitly configured.
	assert.False(t, CanBypassPIIOffNonBedrockPolicy("admin@example.com"))

	SetPIIOffNonBedrockBypassAdmins([]string{"admin@example.com"})
	assert.True(t, CanBypassPIIOffNonBedrockPolicy("admin@example.com"))
	// Case-insensitive + whitespace-tolerant lookup.
	assert.True(t, CanBypassPIIOffNonBedrockPolicy("  Admin@Example.com "))
	assert.False(t, CanBypassPIIOffNonBedrockPolicy("other@example.com"))
}

func TestSetPIIOffNonBedrockBypassAdmins(t *testing.T) {
	// Restore the built-in default after mutating the shared allowlist.
	t.Cleanup(func() { SetPIIOffNonBedrockBypassAdmins(nil) })

	SetPIIOffNonBedrockBypassAdmins([]string{" Custom@Example.com ", "", "two@example.com"})
	assert.True(t, CanBypassPIIOffNonBedrockPolicy("custom@example.com"))
	assert.True(t, CanBypassPIIOffNonBedrockPolicy("two@example.com"))

	// Empty/nil falls back to the built-in default, which ships empty — so
	// an unconfigured deployment grants no bypass at all.
	SetPIIOffNonBedrockBypassAdmins(nil)
	assert.False(t, CanBypassPIIOffNonBedrockPolicy("custom@example.com"))
	assert.False(t, CanBypassPIIOffNonBedrockPolicy("admin@example.com"))
}

func TestShouldEnforceBedrockForPIIOff(t *testing.T) {
	t.Run("global on inherit", func(t *testing.T) {
		assert.False(t, ShouldEnforceBedrockForPIIOff(true, &APIKey{}))
	})
	t.Run("global on explicit off", func(t *testing.T) {
		assert.True(t, ShouldEnforceBedrockForPIIOff(true, &APIKey{RedactPII: boolPtr(false)}))
	})
	t.Run("global off inherit", func(t *testing.T) {
		assert.False(t, ShouldEnforceBedrockForPIIOff(false, &APIKey{}))
	})
	t.Run("global off explicit off", func(t *testing.T) {
		assert.True(t, ShouldEnforceBedrockForPIIOff(false, &APIKey{RedactPII: boolPtr(false)}))
	})
	t.Run("global off explicit on", func(t *testing.T) {
		assert.False(t, ShouldEnforceBedrockForPIIOff(false, &APIKey{RedactPII: boolPtr(true)}))
	})
}

func TestValidatePIIOffBedrockPolicy(t *testing.T) {
	err := ValidatePIIOffBedrockPolicy(true, "openai", boolPtr(false), false)
	require.Error(t, err)

	err = ValidatePIIOffBedrockPolicy(true, "bedrock", boolPtr(false), false)
	require.NoError(t, err)

	err = ValidatePIIOffBedrockPolicy(true, "openai", boolPtr(false), true)
	require.NoError(t, err)

	err = ValidatePIIOffBedrockPolicy(false, "openai", nil, false)
	require.NoError(t, err)
}

func TestEnforcePIIOffBedrockProvider(t *testing.T) {
	key := &APIKey{Provider: "openai", RedactPII: boolPtr(false)}
	require.Error(t, EnforcePIIOffBedrockProvider(true, key))

	key.Provider = BedrockProvider
	require.NoError(t, EnforcePIIOffBedrockProvider(true, key))
}
