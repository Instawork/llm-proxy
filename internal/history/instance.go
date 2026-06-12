package history

import (
	"os"
	"regexp"
	"strings"
)

const envInstanceID = "HISTORY_INSTANCE_ID"

var sanitizeID = regexp.MustCompile(`[^a-z0-9-]+`)

// ResolveInstanceID picks a stable per-process identifier for chunk filenames.
// Priority: HISTORY_INSTANCE_ID env → HOSTNAME → os.Hostname().
func ResolveInstanceID(configured string) string {
	if configured != "" {
		return sanitizeInstanceID(configured)
	}
	if v := os.Getenv(envInstanceID); v != "" {
		return sanitizeInstanceID(v)
	}
	if v := os.Getenv("HOSTNAME"); v != "" {
		return sanitizeInstanceID(v)
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown"
	}
	return sanitizeInstanceID(host)
}

func sanitizeInstanceID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "unknown"
	}
	s = sanitizeID.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "unknown"
	}
	if len(s) > 64 {
		return s[:64]
	}
	return s
}
