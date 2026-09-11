package notify

import (
	"context"
	"log/slog"
)

// LogMailer logs outgoing emails instead of sending them. Used in dev/test
// environments where no SendGrid API key is configured.
type LogMailer struct {
	logger *slog.Logger
}

// NewLogMailer builds a LogMailer.
func NewLogMailer(logger *slog.Logger) *LogMailer {
	return &LogMailer{logger: logger}
}

// Send logs msg at Info level and always succeeds.
func (m *LogMailer) Send(_ context.Context, msg Message) error {
	m.logger.Info("notify: email (dev_log)", "to", msg.To, "subject", msg.Subject, "text", msg.Text)
	return nil
}
