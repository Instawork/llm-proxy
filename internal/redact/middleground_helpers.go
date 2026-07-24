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
	ip := net.ParseIP(trimmed)
	if ip == nil && strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
		// Bracketed IPv6 without a port, e.g. "[::1]".
		ip = net.ParseIP(trimmed[1 : len(trimmed)-1])
	}
	if ip == nil {
		// host:port — SplitHostPort handles IPv4, [IPv6]:port, and
		// hostname:port. Cutting at the first ':' (the old behavior)
		// truncated bare IPv6 addresses ("fd12:3456::1" became "fd12"),
		// so private IPv6 was never recognized and got over-masked.
		if host, _, err := net.SplitHostPort(trimmed); err == nil {
			if host == "localhost" {
				return true
			}
			ip = net.ParseIP(host)
		}
	}
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
