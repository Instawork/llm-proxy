package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
)

// sendTimeout bounds each fire-and-forget send. The triggering request's
// context ends when the response is written, well before an email would go
// out, so every send runs against its own background context.
const sendTimeout = 15 * time.Second

// AdminLister returns the admin dashboard roster, used to find recipients
// for new-key-request notifications. Satisfied by *adminusers.Store.
type AdminLister interface {
	ListUsers(ctx context.Context) ([]adminusers.User, error)
}

// OnceMarker atomically claims a name for ttl, returning true the first time
// (and false on every subsequent call until it expires). Satisfied by
// *adminrollup.Store. A nil OnceMarker falls back to process-local dedupe
// only, which is sufficient for a single-instance dev/test run but not
// cluster-wide.
type OnceMarker interface {
	TryMarkOnce(ctx context.Context, name string, ttl time.Duration) (bool, error)
}

// Notifier sends the proxy's transactional emails. A nil *Notifier is safe
// to call; every method becomes a no-op so callers do not need to guard on
// whether email alerts are configured.
type Notifier struct {
	mailer       Mailer
	admins       AdminLister
	once         OnceMarker
	dashboardURL string
	logger       *slog.Logger

	localOnce sync.Map // string -> struct{}, in-process dedupe ahead of `once`
}

// NewNotifier builds a Notifier. admins, once, and dashboardURL may be left
// zero-valued: admin lookups are then skipped, dedupe is process-local only,
// and dashboard links are omitted from email bodies.
func NewNotifier(mailer Mailer, admins AdminLister, once OnceMarker, dashboardURL string, logger *slog.Logger) *Notifier {
	return &Notifier{
		mailer:       mailer,
		admins:       admins,
		once:         once,
		dashboardURL: strings.TrimRight(dashboardURL, "/"),
		logger:       logger,
	}
}

// send fires msg on a background goroutine so callers never block on
// SendGrid latency or errors.
func (n *Notifier) send(msg Message) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if err := n.mailer.Send(ctx, msg); err != nil {
			n.logger.Error("notify: send failed", "to", msg.To, "subject", msg.Subject, "error", err)
		}
	}()
}

// tryOnce reports whether name has already fired within ttl, marking it as
// fired when it has not. It checks (and sets) the in-process map first so a
// hot key never round-trips to the shared store more than once per process.
func (n *Notifier) tryOnce(ctx context.Context, name string, ttl time.Duration) bool {
	if _, loaded := n.localOnce.LoadOrStore(name, struct{}{}); loaded {
		return false
	}
	if n.once == nil {
		return true
	}
	ok, err := n.once.TryMarkOnce(ctx, name, ttl)
	if err != nil {
		n.logger.Error("notify: dedupe check failed, allowing send", "name", name, "error", err)
		return true
	}
	return ok
}

// KeyRequested notifies every admin that a new key request needs review.
func (n *Notifier) KeyRequested(ctx context.Context, req apikeys.KeyRequest) {
	if n == nil {
		return
	}
	recipients := n.adminEmails(ctx)
	if len(recipients) == 0 {
		n.logger.Warn("notify: no admin recipients for key request", "requester", req.RequesterEmail)
		return
	}

	var body strings.Builder
	fmt.Fprintf(&body, "%s requested a new %s API key.\n\n", req.RequesterEmail, req.Provider)
	fmt.Fprintf(&body, "Description: %s\n", req.Description)
	if req.DailyCostLimit > 0 {
		fmt.Fprintf(&body, "Requested daily limit: $%.2f\n", float64(req.DailyCostLimit)/100)
	}
	if n.dashboardURL != "" {
		fmt.Fprintf(&body, "\nReview it at %s/keys\n", n.dashboardURL)
	}

	n.send(Message{
		To:      recipients,
		Subject: fmt.Sprintf("New LLM proxy key request from %s", req.RequesterEmail),
		Text:    body.String(),
	})
}

// KeyRequestApproved notifies the requester that their key request was
// approved and their key is ready.
func (n *Notifier) KeyRequestApproved(ctx context.Context, req apikeys.KeyRequest, key *apikeys.APIKey) {
	if n == nil {
		return
	}
	_ = ctx
	if strings.TrimSpace(req.RequesterEmail) == "" {
		return
	}

	var body strings.Builder
	fmt.Fprintf(&body, "Your %s API key request has been approved.\n\n", req.Provider)
	fmt.Fprintf(&body, "Description: %s\n", req.Description)
	if key != nil {
		fmt.Fprintf(&body, "Key: %s\n", apikeys.RedactKey(key.PK))
	}
	if n.dashboardURL != "" {
		fmt.Fprintf(&body, "\nRetrieve it at %s/keys\n", n.dashboardURL)
	}

	n.send(Message{
		To:      []string{req.RequesterEmail},
		Subject: fmt.Sprintf("Your %s API key request was approved", req.Provider),
		Text:    body.String(),
	})
}

// SpendLimitReached notifies a key's owner (or, for org keys, the original
// requester) that its daily or monthly cost limit was just hit. window is
// "daily" or "monthly". Sends at most once per key per window per period
// (calendar day or month).
func (n *Notifier) SpendLimitReached(ctx context.Context, rec *apikeys.APIKey, window string, limitCents, spendCents int64) {
	if n == nil || rec == nil {
		return
	}
	recipient := strings.TrimSpace(rec.OwnerEmail)
	if recipient == "" {
		recipient = strings.TrimSpace(rec.RequesterEmail)
	}
	if recipient == "" {
		n.logger.Warn("notify: spend limit reached but key has no owner/requester email", "key", apikeys.RedactKey(rec.PK))
		return
	}

	period := time.Now().UTC().Format("2006-01-02")
	ttl := 25 * time.Hour
	if window == "monthly" {
		period = time.Now().UTC().Format("2006-01")
		ttl = 32 * 24 * time.Hour
	}
	dedupeName := fmt.Sprintf("spend-alert:%s:%s:%s", apikeys.RedactKey(rec.PK), window, period)
	if !n.tryOnce(ctx, dedupeName, ttl) {
		return
	}

	body := fmt.Sprintf(
		"Your API key (%s) has reached its %s cost limit of $%.2f.\n\nCurrent spend: $%.2f\n",
		apikeys.RedactKey(rec.PK), window, float64(limitCents)/100, float64(spendCents)/100,
	)
	if n.dashboardURL != "" {
		body += fmt.Sprintf("\nView usage at %s/keys\n", n.dashboardURL)
	}

	n.send(Message{
		To:      []string{recipient},
		Subject: fmt.Sprintf("LLM proxy key hit its %s spend limit", window),
		Text:    body,
	})
}

func (n *Notifier) adminEmails(ctx context.Context) []string {
	if n.admins == nil {
		return nil
	}
	users, err := n.admins.ListUsers(ctx)
	if err != nil {
		n.logger.Error("notify: list admin users failed", "error", err)
		return nil
	}
	emails := make([]string, 0, len(users))
	for _, u := range users {
		if u.Role == adminusers.RoleAdmin && strings.TrimSpace(u.Email) != "" {
			emails = append(emails, u.Email)
		}
	}
	return emails
}
