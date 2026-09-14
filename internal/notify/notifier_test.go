package notify

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSender struct {
	mu   sync.Mutex
	sent []Notification
	sig  chan struct{}
}

func newFakeSender() *fakeSender {
	return &fakeSender{sig: make(chan struct{}, 16)}
}

func (m *fakeSender) Send(_ context.Context, msg Notification) error {
	m.mu.Lock()
	m.sent = append(m.sent, msg)
	m.mu.Unlock()
	m.sig <- struct{}{}
	return nil
}

func (m *fakeSender) waitForSend(t *testing.T) {
	t.Helper()
	select {
	case <-m.sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for email send")
	}
}

func (m *fakeSender) messages() []Notification {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Notification, len(m.sent))
	copy(out, m.sent)
	return out
}

type fakeAdminLister struct {
	users []adminusers.User
}

func (f *fakeAdminLister) ListUsers(context.Context) ([]adminusers.User, error) {
	return f.users, nil
}

type fakeOnceMarker struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newFakeOnceMarker() *fakeOnceMarker {
	return &fakeOnceMarker{seen: map[string]bool{}}
}

func (f *fakeOnceMarker) TryMarkOnce(_ context.Context, name string, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen[name] {
		return false, nil
	}
	f.seen[name] = true
	return true, nil
}

func TestNotifier_KeyRequested_OnlyAdmins(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	admins := &fakeAdminLister{users: []adminusers.User{
		{Email: "admin@example.com", Role: adminusers.RoleAdmin},
		{Email: "editor@example.com", Role: adminusers.RoleEditor},
		{Email: "viewer@example.com", Role: adminusers.RoleViewer},
	}}
	n := NewNotifier(sender, admins, nil, "https://llm.example.com", testLogger())

	n.KeyRequested(context.Background(), apikeys.KeyRequest{
		RequesterEmail: "requester@example.com",
		Provider:       "openai",
		Description:    "for widget bot",
		DailyCostLimit: 1000,
	})
	sender.waitForSend(t)

	msgs := sender.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, KindKeyRequested, msgs[0].Kind)
	assert.Equal(t, []string{"admin@example.com"}, msgs[0].To)
	assert.Contains(t, msgs[0].Title, "requester@example.com")
	assert.Contains(t, msgs[0].Body, "openai")
}

func TestNotifier_KeyRequested_NoAdmins_DoesNotSend(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	admins := &fakeAdminLister{users: []adminusers.User{{Email: "viewer@example.com", Role: adminusers.RoleViewer}}}
	n := NewNotifier(sender, admins, nil, "", testLogger())

	n.KeyRequested(context.Background(), apikeys.KeyRequest{RequesterEmail: "requester@example.com", Provider: "openai"})

	select {
	case <-sender.sig:
		t.Fatal("expected no send with zero admin recipients")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotifier_KeyRequestApproved_UsesRedactedKey(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	n := NewNotifier(sender, nil, nil, "https://llm.example.com", testLogger())

	key := &apikeys.APIKey{PK: "sk-iw-" + "abcdefghijklmnopqrstuvwxyz0123456789"}
	n.KeyRequestApproved(context.Background(), apikeys.KeyRequest{
		RequesterEmail: "requester@example.com",
		Provider:       "anthropic",
		Description:    "widget bot",
	}, key)
	sender.waitForSend(t)

	msgs := sender.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, KindKeyRequestApproved, msgs[0].Kind)
	assert.Equal(t, []string{"requester@example.com"}, msgs[0].To)
	assert.NotContains(t, msgs[0].Body, key.PK)
	assert.Contains(t, msgs[0].Body, apikeys.RedactKey(key.PK))
}

func TestNotifier_SpendLimitReached_PrefersOwnerThenRequester(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	n := NewNotifier(sender, nil, newFakeOnceMarker(), "", testLogger())

	owned := &apikeys.APIKey{PK: "sk-iw-owned0000000000000000000000000000", OwnerEmail: "owner@example.com", RequesterEmail: "requester@example.com"}
	n.SpendLimitReached(context.Background(), owned, "daily", 1000, 1000)
	sender.waitForSend(t)
	msgs := sender.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, KindSpendLimitReached, msgs[0].Kind)
	assert.Equal(t, []string{"owner@example.com"}, msgs[0].To)

	requested := &apikeys.APIKey{PK: "sk-iw-requested000000000000000000000000", RequesterEmail: "requester@example.com"}
	n.SpendLimitReached(context.Background(), requested, "monthly", 5000, 5000)
	sender.waitForSend(t)
	msgs = sender.messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, []string{"requester@example.com"}, msgs[1].To)
}

func TestNotifier_SpendLimitReached_NoRecipient_DoesNotSend(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	n := NewNotifier(sender, nil, newFakeOnceMarker(), "", testLogger())

	n.SpendLimitReached(context.Background(), &apikeys.APIKey{PK: "sk-iw-none0000000000000000000000000000"}, "daily", 1000, 1000)

	select {
	case <-sender.sig:
		t.Fatal("expected no send when key has neither owner nor requester email")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotifier_SpendLimitReached_DedupesPerKeyAndWindow(t *testing.T) {
	t.Parallel()

	sender := newFakeSender()
	n := NewNotifier(sender, nil, newFakeOnceMarker(), "", testLogger())
	key := &apikeys.APIKey{PK: "sk-iw-dedupe00000000000000000000000000", OwnerEmail: "owner@example.com"}

	n.SpendLimitReached(context.Background(), key, "daily", 1000, 1000)
	sender.waitForSend(t)

	// Second daily block for the same key/day must not send again.
	n.SpendLimitReached(context.Background(), key, "daily", 1000, 1000)
	select {
	case <-sender.sig:
		t.Fatal("expected dedupe to suppress a repeat daily alert")
	case <-time.After(200 * time.Millisecond):
	}

	// A different window (monthly) is a distinct dedupe key and should send.
	n.SpendLimitReached(context.Background(), key, "monthly", 5000, 5000)
	sender.waitForSend(t)

	assert.Len(t, sender.messages(), 2)
}

func TestNotifier_NilReceiver_IsNoOp(t *testing.T) {
	t.Parallel()
	var n *Notifier
	n.KeyRequested(context.Background(), apikeys.KeyRequest{})
	n.KeyRequestApproved(context.Background(), apikeys.KeyRequest{}, nil)
	n.SpendLimitReached(context.Background(), &apikeys.APIKey{OwnerEmail: "a@example.com"}, "daily", 1, 1)
}
