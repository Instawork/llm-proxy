package keyexpiry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

type fakeStore struct {
	expired      []*apikeys.APIKey
	listErr      error
	deleteErr    map[string]error
	deleted      []string
	leaseGranted bool
	leaseErr     error
}

func (f *fakeStore) ListExpiredKeys(ctx context.Context, cutoff time.Time) ([]*apikeys.APIKey, error) {
	return f.expired, f.listErr
}

func (f *fakeStore) DeleteKey(ctx context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return f.deleteErr[key]
}

func (f *fakeStore) TryAcquireSweepLease(ctx context.Context, holder string, ttl time.Duration) (bool, error) {
	return f.leaseGranted, f.leaseErr
}

type fakeRevoker struct {
	revoked []string
	err     error
}

func (f *fakeRevoker) Revoke(ctx context.Context, provider, upstreamID, upstreamKind string) error {
	f.revoked = append(f.revoked, provider+":"+upstreamID)
	return f.err
}

func key(pk string, provisioned bool) *apikeys.APIKey {
	return &apikeys.APIKey{
		PK:            pk,
		Provider:      "openai",
		Provisioned:   provisioned,
		UpstreamKeyID: "upstream-" + pk,
	}
}

func TestSweeper_SweepOnce_NoLease_NoWork(t *testing.T) {
	store := &fakeStore{leaseGranted: false, expired: []*apikeys.APIKey{key("a", true)}}
	rev := &fakeRevoker{}
	s := NewSweeper(store, rev, Config{}, nil)

	deleted, err := s.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
	assert.Empty(t, store.deleted, "losing the lease must not touch any keys")
	assert.Empty(t, rev.revoked)
}

func TestSweeper_SweepOnce_RevokesProvisionedOnly(t *testing.T) {
	store := &fakeStore{
		leaseGranted: true,
		expired:      []*apikeys.APIKey{key("provisioned", true), key("byo", false)},
	}
	rev := &fakeRevoker{}
	s := NewSweeper(store, rev, Config{}, nil)

	deleted, err := s.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.ElementsMatch(t, []string{"provisioned", "byo"}, store.deleted)
	assert.Equal(t, []string{"openai:upstream-provisioned"}, rev.revoked, "only the provisioned key should trigger a revoke")
}

func TestSweeper_SweepOnce_DeletesEvenWhenRevokeFails(t *testing.T) {
	store := &fakeStore{leaseGranted: true, expired: []*apikeys.APIKey{key("a", true)}}
	rev := &fakeRevoker{err: errors.New("vendor unavailable")}
	s := NewSweeper(store, rev, Config{}, nil)

	deleted, err := s.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "a best-effort revoke failure must not block deletion")
	assert.Equal(t, []string{"a"}, store.deleted)
}

func TestSweeper_SweepOnce_ContinuesAfterOneDeleteFails(t *testing.T) {
	store := &fakeStore{
		leaseGranted: true,
		expired:      []*apikeys.APIKey{key("bad", false), key("good", false)},
		deleteErr:    map[string]error{"bad": errors.New("conditional check failed")},
	}
	rev := &fakeRevoker{}
	s := NewSweeper(store, rev, Config{}, nil)

	deleted, err := s.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "one failed delete should not stop the rest of the sweep")
	assert.ElementsMatch(t, []string{"bad", "good"}, store.deleted)
}

func TestSweeper_SweepOnce_ListErrorPropagates(t *testing.T) {
	store := &fakeStore{leaseGranted: true, listErr: errors.New("scan failed")}
	s := NewSweeper(store, &fakeRevoker{}, Config{}, nil)

	_, err := s.SweepOnce(context.Background())
	require.Error(t, err)
}

func TestSweeper_SweepOnce_NilRevokerSkipsRevoke(t *testing.T) {
	store := &fakeStore{leaseGranted: true, expired: []*apikeys.APIKey{key("a", true)}}
	s := NewSweeper(store, nil, Config{}, nil)

	deleted, err := s.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
}

func TestConfig_Defaults(t *testing.T) {
	var cfg Config
	assert.Equal(t, defaultSweepInterval, cfg.sweepInterval())
	assert.Equal(t, defaultGracePeriod, cfg.gracePeriod())

	cfg = Config{SweepInterval: 5 * time.Minute, GracePeriod: 3 * 24 * time.Hour}
	assert.Equal(t, 5*time.Minute, cfg.sweepInterval())
	assert.Equal(t, 3*24*time.Hour, cfg.gracePeriod())
}
