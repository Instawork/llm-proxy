package proxylog

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestProxyAndUpstreamPrintf(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(log.Writer())

	Proxy("auth failed: %s", "missing")
	Upstream("openai transport: %v", "timeout")

	out := buf.String()
	if !strings.Contains(out, "[PROXY] auth failed: missing") {
		t.Fatalf("unexpected proxy line: %q", out)
	}
	if !strings.Contains(out, "[UPSTREAM] openai transport: timeout") {
		t.Fatalf("unexpected upstream line: %q", out)
	}
}

func TestSlogPrefixes(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	SlogProxy(logger, slog.LevelError, "circuit store error", "key", "openai")
	SlogUpstream(logger, slog.LevelWarn, "terminal failure", "status", 503)

	out := buf.String()
	if !strings.Contains(out, `"msg":"[PROXY] circuit store error"`) {
		t.Fatalf("missing proxy slog prefix: %q", out)
	}
	if !strings.Contains(out, `"msg":"[UPSTREAM] terminal failure"`) {
		t.Fatalf("missing upstream slog prefix: %q", out)
	}
}
