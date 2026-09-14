// Package notify sends transactional notifications (spend limits, key
// requests, key-request approvals) via a pluggable channel — currently
// email through SendGrid or Amazon SES.
package notify

import "context"

// Kind identifies which event triggered a notification.
type Kind string

const (
	KindKeyRequested       Kind = "key_requested"
	KindKeyRequestApproved Kind = "key_request_approved"
	KindSpendLimitReached  Kind = "spend_limit_reached"
)

// Notification is a channel-agnostic message to one or more recipients.
// Adapters map To/Title/Body onto their wire format (e.g. email To/Subject/Text).
type Notification struct {
	Kind  Kind
	To    []string
	Title string
	Body  string
}

// Sender delivers a single notification. Implementations must be safe for
// concurrent use.
type Sender interface {
	Send(ctx context.Context, n Notification) error
}
