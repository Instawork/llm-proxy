package adminrollup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsMarkedAndReleaseMark(t *testing.T) {
	forEachBackend(t, func(t *testing.T, store *Store) {
		ctx := context.Background()

		marked, err := store.IsMarked(ctx, "seed:a")
		require.NoError(t, err)
		require.False(t, marked)

		ok, err := store.TryMarkOnce(ctx, "seed:a", time.Hour)
		require.NoError(t, err)
		require.True(t, ok)

		marked, err = store.IsMarked(ctx, "seed:a")
		require.NoError(t, err)
		require.True(t, marked)
		marked, err = store.IsMarked(ctx, "seed:b")
		require.NoError(t, err)
		require.False(t, marked, "marks are per name")

		ok, err = store.TryMarkOnce(ctx, "seed:a", time.Hour)
		require.NoError(t, err)
		require.False(t, ok)

		require.NoError(t, store.ReleaseMark(ctx, "seed:a"))
		marked, err = store.IsMarked(ctx, "seed:a")
		require.NoError(t, err)
		require.False(t, marked)
		ok, err = store.TryMarkOnce(ctx, "seed:a", time.Hour)
		require.NoError(t, err)
		require.True(t, ok, "a released mark can be claimed again")
	})
}

func TestMarkHelpers_NilStore(t *testing.T) {
	var store *Store
	marked, err := store.IsMarked(context.Background(), "x")
	require.NoError(t, err)
	require.False(t, marked)
	require.NoError(t, store.ReleaseMark(context.Background(), "x"))
}
