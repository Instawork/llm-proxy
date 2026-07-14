// Package proxylog standardizes [PROXY] vs [UPSTREAM] prefixes on error logs
// and HTTP error responses so operators and clients can tell proxy-local
// failures apart from upstream provider failures.
package proxylog

import (
	"context"
	"log"
	"log/slog"
)

const (
	// TagProxy marks errors originating inside llm-proxy (auth, routing,
	// middleware, response parsing, circuit state, etc.).
	TagProxy = "[PROXY]"
	// TagUpstream marks errors from or about the upstream LLM provider
	// (transport failures, provider HTTP errors, rate limits, etc.).
	TagUpstream = "[UPSTREAM]"
)

// Proxy logs a proxy-local error via the standard library logger.
func Proxy(format string, args ...any) {
	log.Printf(TagProxy+" "+format, args...)
}

// Upstream logs an upstream-provider error via the standard library logger.
func Upstream(format string, args ...any) {
	log.Printf(TagUpstream+" "+format, args...)
}

// ProxyMsg prefixes a message for structured slog logging.
func ProxyMsg(msg string) string {
	return TagProxy + " " + msg
}

// UpstreamMsg prefixes a message for structured slog logging.
func UpstreamMsg(msg string) string {
	return TagUpstream + " " + msg
}

// SlogProxy emits a structured log record for a proxy-local condition.
func SlogProxy(logger *slog.Logger, level slog.Level, msg string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, ProxyMsg(msg), args...)
}

// SlogUpstream emits a structured log record for an upstream condition.
func SlogUpstream(logger *slog.Logger, level slog.Level, msg string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, UpstreamMsg(msg), args...)
}
