package apikeys

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsBYOMaskedKeyID(t *testing.T) {
	hash := CredentialHashSuffix("sk-ant-secret")
	byo := "sk-ant-…" + hash
	assert.True(t, IsBYOMaskedKeyID(byo))
	assert.False(t, IsBYOMaskedKeyID("sk-iw-abc12345678901234567890123456789012"))
	assert.False(t, IsBYOMaskedKeyID(""))
}

func TestInferProviderFromMaskedID(t *testing.T) {
	assert.Equal(t, "anthropic", InferProviderFromMaskedID("sk-ant-…"+CredentialHashSuffix("x")))
	assert.Equal(t, "gemini", InferProviderFromMaskedID("AIza…"+CredentialHashSuffix("x")))
	assert.Equal(t, "openai", InferProviderFromMaskedID("sk-proj-…"+CredentialHashSuffix("x")))
}

func TestBYOKeyLookup(t *testing.T) {
	masked := "sk-ant-…" + CredentialHashSuffix("sk-ant-test")
	lookup, provider, err := BYOKeyLookup("anthropic", masked)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", provider)
	assert.Contains(t, lookup, "anthropic:")
}

func TestValidateBYOBanRequest(t *testing.T) {
	masked := "sk-ant-…" + CredentialHashSuffix("sk-ant-test")
	provider, err := ValidateBYOBanRequest("anthropic", masked)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", provider)

	_, err = ValidateBYOBanRequest("openai", masked)
	assert.Error(t, err)

	gsk := "gsk_…" + CredentialHashSuffix("gsk_test")
	provider, err = ValidateBYOBanRequest("openai", gsk)
	require.NoError(t, err)
	assert.Equal(t, "openai", provider)

	_, err = ValidateBYOBanRequest("", gsk)
	assert.Error(t, err)

	_, err = ValidateBYOBanRequest("openai", "sk-iw-not-byo")
	assert.Error(t, err)
}

func TestInferProviderFromMaskedID_RouteAmbiguous(t *testing.T) {
	assert.Equal(t, "", InferProviderFromMaskedID("gsk_…"+CredentialHashSuffix("x")))
	assert.Equal(t, "", InferProviderFromMaskedID("xai-…"+CredentialHashSuffix("x")))
}
