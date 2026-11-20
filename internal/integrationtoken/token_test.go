package integrationtoken

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTokenFormatAndHash(t *testing.T) {
	token, sum, err := New("init")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(token, "llmpi_init_"))
	assert.False(t, strings.HasPrefix(token, "sk-"))
	assert.Len(t, strings.TrimPrefix(token, "llmpi_init_"), 43, "32 bytes as unpadded base64url")

	want := Hash(token)
	assert.Equal(t, hex.EncodeToString(want[:]), sum)

	other, _, err := New("init")
	require.NoError(t, err)
	assert.NotEqual(t, token, other)
}

func TestNewRejectsInvalidCallerID(t *testing.T) {
	for _, id := range []string{"", "Init", "has space", "under_score", strings.Repeat("a", 33)} {
		_, _, err := New(id)
		assert.Error(t, err, "caller id %q", id)
	}
}

func TestParseHashes(t *testing.T) {
	a := Hash("llmpi_a_x")
	b := Hash("llmpi_b_y")
	got, err := ParseHashes(" " + hex.EncodeToString(a[:]) + " , " + strings.ToUpper(hex.EncodeToString(b[:])) + ",")
	require.NoError(t, err)
	assert.Equal(t, [][32]byte{a, b}, got)

	for _, bad := range []string{"", " , ", "abc", hex.EncodeToString(a[:16]), "zz" + hex.EncodeToString(a[1:])} {
		_, err := ParseHashes(bad)
		assert.Error(t, err, "value %q", bad)
	}
}
