package keyexpiry

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetireKey_NonProvisioned_SkipsRevoke(t *testing.T) {
	store := &fakeStore{}
	rev := &fakeRevoker{}

	err := RetireKey(context.Background(), store, rev, key("k1", false), slog.Default())
	require.NoError(t, err)
	assert.Empty(t, rev.revoked)
	assert.Equal(t, []string{"k1"}, store.deleted)
}

func TestRetireKey_Provisioned_RevokesThenDeletes(t *testing.T) {
	store := &fakeStore{}
	rev := &fakeRevoker{}

	err := RetireKey(context.Background(), store, rev, key("k2", true), slog.Default())
	require.NoError(t, err)
	assert.Equal(t, []string{"openai:upstream-k2"}, rev.revoked)
	assert.Equal(t, []string{"k2"}, store.deleted)
}

func TestRetireKey_RevokeErrorDoesNotBlockDelete(t *testing.T) {
	store := &fakeStore{}
	rev := &fakeRevoker{err: errors.New("upstream down")}

	err := RetireKey(context.Background(), store, rev, key("k3", true), slog.Default())
	require.NoError(t, err)
	assert.Equal(t, []string{"k3"}, store.deleted)
}

func TestRetireKey_DeleteErrorPropagates(t *testing.T) {
	store := &fakeStore{deleteErr: map[string]error{"k4": errors.New("not found")}}

	err := RetireKey(context.Background(), store, nil, key("k4", false), slog.Default())
	require.Error(t, err)
}
