package proxylog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyHTTPError(t *testing.T) {
	rec := httptest.NewRecorder()
	ProxyHTTPError(rec, "rate limit exceeded", http.StatusTooManyRequests)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderErrorSource); got != ErrorSourceProxy {
		t.Fatalf("source = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "[PROXY] rate limit exceeded") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestWriteUpstreamJSONError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteUpstreamJSONError(rec, http.StatusBadGateway, "openai transport: timeout")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderErrorSource); got != ErrorSourceUpstream {
		t.Fatalf("source = %q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "[UPSTREAM] openai transport: timeout" {
		t.Fatalf("error = %q", body["error"])
	}
}

func TestUpstreamSSEDataLine(t *testing.T) {
	line := UpstreamSSEDataLine("bedrock transport: %v", "reset")
	if !strings.Contains(line, "[UPSTREAM] bedrock transport: reset") {
		t.Fatalf("line = %q", line)
	}
}
