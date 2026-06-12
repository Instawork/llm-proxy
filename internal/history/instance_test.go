package history

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveInstanceID_EnvOverride(t *testing.T) {
	t.Setenv("HISTORY_INSTANCE_ID", "task-abc-123")
	require.Equal(t, "task-abc-123", ResolveInstanceID(""))
}

func TestResolveInstanceID_Configured(t *testing.T) {
	require.Equal(t, "my-id", ResolveInstanceID("my-id"))
}

func TestResolveInstanceID_Sanitize(t *testing.T) {
	require.Equal(t, "abc-123", ResolveInstanceID("ABC_123!!!"))
}

func TestStreamEnabled(t *testing.T) {
	require.True(t, StreamEnabled(nil, StreamCost))
	require.True(t, StreamEnabled([]string{"cost", "pii"}, StreamPII))
	require.False(t, StreamEnabled([]string{"cost"}, StreamPII))
}
