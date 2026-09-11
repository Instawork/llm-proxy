// Package notify sends transactional email alerts (spend limits, key
// requests, key-request approvals) via SendGrid.
package notify

import "context"

// Message is a plain-text email to one or more recipients.
type Message struct {
	To      []string
	Subject string
	Text    string
}

// Mailer sends a single email. Implementations must be safe for concurrent use.
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}
