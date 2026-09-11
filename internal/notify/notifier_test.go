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

type fakeMailer struct {
	mu   sync.Mutex
	sent []Message
	sig  chan struct{}
}

func newFakeMailer() *fakeMailer {
	return &fakeMailer{sig: make(chan struct{}, 16)}
}

func (m *fakeMailer) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	m.sent = append(m.sent, msg)
	m.mu.Unlock()
	m.sig <- struct{}{}
	return nil
}

func (m *fakeMailer) waitForSend(t *testing.T) {
	t.Helper()
	select {
	case <-m.sig:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for email send")
	}
}

func (m *fakeMailer) messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Message, len(m.sent))
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

	mailer := newFakeMailer()
	admins := &fakeAdminLister{users: []adminusers.User{
		{Email: "admin@example.com", Role: adminusers.RoleAdmin},
		{Email: "editor@example.com", Role: adminusers.RoleEditor},
		{Email: "viewer@example.com", Role: adminusers.RoleViewer},
	}}
	n := NewNotifier(mailer, admins, nil, "https://llm.example.com", testLogger())

	n.KeyRequested(context.Background(), apikeys.KeyRequest{
		RequesterEmail: "requester@example.com",
		Provider:       "openai",
		Description:    "for widget bot",
		DailyCostLimit: 1000,
	})
	mailer.waitForSend(t)

	msgs := mailer.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"admin@example.com"}, msgs[0].To)
	assert.Contains(t, msgs[0].Subject, "requester@example.com")
	assert.Contains(t, msgs[0].Text, "openai")
}

func TestNotifier_KeyRequested_NoAdmins_DoesNotSend(t *testing.T) {
	t.Parallel()

	mailer := newFakeMailer()
	admins := &fakeAdminLister{users: []adminusers.User{{Email: "viewer@example.com", Role: adminusers.RoleViewer}}}
	n := NewNotifier(mailer, admins, nil, "", testLogger())

	n.KeyRequested(context.Background(), apikeys.KeyRequest{RequesterEmail: "requester@example.com", Provider: "openai"})

	select {
	case <-mailer.sig:
		t.Fatal("expected no send with zero admin recipients")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotifier_KeyRequestApproved_UsesRedactedKey(t *testing.T) {
	t.Parallel()

	mailer := newFakeMailer()
	n := NewNotifier(mailer, nil, nil, "https://llm.example.com", testLogger())

	key := &apikeys.APIKey{PK: "sk-iw-" + "abcdefghijklmnopqrstuvwxyz0123456789"}
	n.KeyRequestApproved(context.Background(), apikeys.KeyRequest{
		RequesterEmail: "requester@example.com",
		Provider:       "anthropic",
		Description:    "widget bot",
	}, key)
	mailer.waitForSend(t)

	msgs := mailer.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"requester@example.com"}, msgs[0].To)
	assert.NotContains(t, msgs[0].Text, key.PK)
	assert.Contains(t, msgs[0].Text, apikeys.RedactKey(key.PK))
}

func TestNotifier_SpendLimitReached_PrefersOwnerThenRequester(t *testing.T) {
	t.Parallel()

	mailer := newFakeMailer()
	n := NewNotifier(mailer, nil, newFakeOnceMarker(), "", testLogger())

	owned := &apikeys.APIKey{PK: "sk-iw-owned0000000000000000000000000000", OwnerEmail: "owner@example.com", RequesterEmail: "requester@example.com"}
	n.SpendLimitReached(context.Background(), owned, "daily", 1000, 1000)
	mailer.waitForSend(t)
	msgs := mailer.messages()
	require.Len(t, msgs, 1)
	assert.Equal(t, []string{"owner@example.com"}, msgs[0].To)

	requested := &apikeys.APIKey{PK: "sk-iw-requested000000000000000000000000", RequesterEmail: "requester@example.com"}
	n.SpendLimitReached(context.Background(), requested, "monthly", 5000, 5000)
	mailer.waitForSend(t)
	msgs = mailer.messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, []string{"requester@example.com"}, msgs[1].To)
}

func TestNotifier_SpendLimitReached_NoRecipient_DoesNotSend(t *testing.T) {
	t.Parallel()

	mailer := newFakeMailer()
	n := NewNotifier(mailer, nil, newFakeOnceMarker(), "", testLogger())

	n.SpendLimitReached(context.Background(), &apikeys.APIKey{PK: "sk-iw-none0000000000000000000000000000"}, "daily", 1000, 1000)

	select {
	case <-mailer.sig:
		t.Fatal("expected no send when key has neither owner nor requester email")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotifier_SpendLimitReached_DedupesPerKeyAndWindow(t *testing.T) {
	t.Parallel()

	mailer := newFakeMailer()
	n := NewNotifier(mailer, nil, newFakeOnceMarker(), "", testLogger())
	key := &apikeys.APIKey{PK: "sk-iw-dedupe00000000000000000000000000", OwnerEmail: "owner@example.com"}

	n.SpendLimitReached(context.Background(), key, "daily", 1000, 1000)
	mailer.waitForSend(t)

	// Second daily block for the same key/day must not send again.
	n.SpendLimitReached(context.Background(), key, "daily", 1000, 1000)
	select {
	case <-mailer.sig:
		t.Fatal("expected dedupe to suppress a repeat daily alert")
	case <-time.After(200 * time.Millisecond):
	}

	// A different window (monthly) is a distinct dedupe key and should send.
	n.SpendLimitReached(context.Background(), key, "monthly", 5000, 5000)
	mailer.waitForSend(t)

	assert.Len(t, mailer.messages(), 2)
}

func TestNotifier_NilReceiver_IsNoOp(t *testing.T) {
	t.Parallel()
	var n *Notifier
	n.KeyRequested(context.Background(), apikeys.KeyRequest{})
	n.KeyRequestApproved(context.Background(), apikeys.KeyRequest{}, nil)
	n.SpendLimitReached(context.Background(), &apikeys.APIKey{OwnerEmail: "a@example.com"}, "daily", 1, 1)
}
