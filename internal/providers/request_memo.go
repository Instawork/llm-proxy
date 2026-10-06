package providers

import (
	"context"
	"net/http"
	"sync"
)

// RequestMemo caches request-body-derived facts for the lifetime of one
// proxied request so the middleware chain parses the body once per fact
// instead of once per middleware. Before this, a single chat completion
// request was JSON-decoded into map[string]interface{} roughly ten times:
// five middlewares asked IsStreamingRequest, three or four asked for the
// model (model status, cost estimate, rate-limit estimate, circuit-breaker
// key), and token parsing asked for the user ID.
//
// The memo lives on the request context (see WithRequestMemo /
// RequestMemoMiddleware in the middleware package). Every accessor degrades
// to the uncached provider call when no memo is present, so handlers mounted
// outside the chain keep working unchanged.
//
// Body rewrites (PII redaction, ID gate) must call InvalidateRequestMemo so
// message text derived before the rewrite is not reused after it. Model and
// stream flags are unaffected by redaction, but invalidating everything is
// cheap and removes a class of ordering bugs.
type RequestMemo struct {
	mu sync.Mutex

	streamSet bool
	stream    bool

	modelSet bool
	model    string
	messages []string

	userIDSet bool
	userID    string
}

type requestMemoKey struct{}

// WithRequestMemo returns ctx carrying a fresh RequestMemo.
func WithRequestMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestMemoKey{}, &RequestMemo{})
}

// RequestMemoFrom returns the memo on ctx, or nil when none is attached.
func RequestMemoFrom(ctx context.Context) *RequestMemo {
	if ctx == nil {
		return nil
	}
	m, _ := ctx.Value(requestMemoKey{}).(*RequestMemo)
	return m
}

// InvalidateRequestMemo drops every cached fact on the request's memo. Call
// it after replacing req.Body with different bytes.
func InvalidateRequestMemo(req *http.Request) {
	if req == nil {
		return
	}
	if m := RequestMemoFrom(req.Context()); m != nil {
		m.invalidate()
	}
}

func (m *RequestMemo) invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamSet, m.modelSet, m.userIDSet = false, false, false
	m.stream, m.model, m.messages, m.userID = false, "", nil, ""
}

// RequestIsStreaming is p.IsStreamingRequest(req), memoized per request.
func RequestIsStreaming(p Provider, req *http.Request) bool {
	if p == nil || req == nil {
		return false
	}
	m := RequestMemoFrom(req.Context())
	if m == nil {
		return p.IsStreamingRequest(req)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.streamSet {
		m.stream = p.IsStreamingRequest(req)
		m.streamSet = true
	}
	return m.stream
}

// RequestModelAndMessages is p.ExtractRequestModelAndMessages(req), memoized
// per request. The returned slice is shared between callers and must not be
// mutated.
func RequestModelAndMessages(p Provider, req *http.Request) (string, []string) {
	if p == nil || req == nil {
		return "", nil
	}
	m := RequestMemoFrom(req.Context())
	if m == nil {
		return p.ExtractRequestModelAndMessages(req)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.modelSet {
		m.model, m.messages = p.ExtractRequestModelAndMessages(req)
		m.modelSet = true
	}
	return m.model, m.messages
}

// RequestUserID is p.UserIDFromRequest(req), memoized per request.
func RequestUserID(p Provider, req *http.Request) string {
	if p == nil || req == nil {
		return ""
	}
	m := RequestMemoFrom(req.Context())
	if m == nil {
		return p.UserIDFromRequest(req)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.userIDSet {
		m.userID = p.UserIDFromRequest(req)
		m.userIDSet = true
	}
	return m.userID
}
