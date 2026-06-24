package redact

import (
	"net"
	"strings"
)

func isPrivateOrLoopbackIP(value string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "localhost" {
		return true
	}
	if host, _, ok := strings.Cut(trimmed, ":"); ok && host != "" {
		trimmed = host
	}
	ip := net.ParseIP(trimmed)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}

func isTestEmail(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(lower, "test@") || strings.HasPrefix(lower, "dev@") {
		return true
	}
	for _, domain := range []string{
		"@example.com",
		"@example.org",
		"@example.net",
		"@test.com",
		"@localhost",
	} {
		if strings.HasSuffix(lower, domain) {
			return true
		}
	}
	return false
}
