package proxylog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	// HeaderErrorSource is set on HTTP error responses synthesized by
	// llm-proxy so clients can distinguish proxy-local failures from
	// upstream provider failures without parsing the body.
	HeaderErrorSource = "X-Llm-Proxy-Error-Source"
	// ErrorSourceProxy is the HeaderErrorSource value for proxy-local errors.
	ErrorSourceProxy = "proxy"
	// ErrorSourceUpstream is the HeaderErrorSource value for upstream errors.
	ErrorSourceUpstream = "upstream"
)

func prefixed(tag, msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return tag
	}
	if strings.HasPrefix(msg, tag) {
		return msg
	}
	return tag + " " + msg
}

func setErrorSourceHeader(w http.ResponseWriter, source string) {
	if source != "" {
		w.Header().Set(HeaderErrorSource, source)
	}
}

// ProxyHTTPError writes a plain-text proxy-local HTTP error response.
func ProxyHTTPError(w http.ResponseWriter, msg string, code int) {
	setErrorSourceHeader(w, ErrorSourceProxy)
	http.Error(w, prefixed(TagProxy, msg), code)
}

// UpstreamHTTPError writes a plain-text upstream HTTP error response.
func UpstreamHTTPError(w http.ResponseWriter, msg string, code int) {
	setErrorSourceHeader(w, ErrorSourceUpstream)
	http.Error(w, prefixed(TagUpstream, msg), code)
}

// WriteProxyJSONError writes {"error":"<msg>"} for a proxy-local failure.
func WriteProxyJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSONError(w, code, prefixed(TagProxy, msg), ErrorSourceProxy)
}

// WriteUpstreamJSONError writes {"error":"<msg>"} for an upstream failure.
func WriteUpstreamJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSONError(w, code, prefixed(TagUpstream, msg), ErrorSourceUpstream)
}

func writeJSONError(w http.ResponseWriter, code int, msg, source string) {
	w.Header().Set("Content-Type", "application/json")
	setErrorSourceHeader(w, source)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// UpstreamPlain formats a plain-text upstream error body.
func UpstreamPlain(format string, args ...any) string {
	return prefixed(TagUpstream, fmt.Sprintf(format, args...))
}

// ProxyPlain formats a plain-text proxy-local error body.
func ProxyPlain(format string, args ...any) string {
	return prefixed(TagProxy, fmt.Sprintf(format, args...))
}

// UpstreamSSEDataLine returns an SSE data line with a JSON error object.
func UpstreamSSEDataLine(format string, args ...any) string {
	payload, _ := json.Marshal(map[string]string{
		"error": UpstreamPlain(format, args...),
	})
	return fmt.Sprintf("data: %s\n\n", payload)
}
