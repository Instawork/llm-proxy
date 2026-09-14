package notify

import (
	"context"
	"log/slog"
)

// LogSender logs outgoing notifications instead of sending them. Used in
// dev/test environments where no real provider is configured.
type LogSender struct {
	logger *slog.Logger
}

// NewLogSender builds a LogSender.
func NewLogSender(logger *slog.Logger) *LogSender {
	return &LogSender{logger: logger}
}

// Send logs n at Info level and always succeeds.
func (s *LogSender) Send(_ context.Context, n Notification) error {
	s.logger.Info("notify: notification (log)", "kind", n.Kind, "to", n.To, "title", n.Title, "body", n.Body)
	return nil
}
