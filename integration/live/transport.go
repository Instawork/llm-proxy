package live

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// capturingTransport records the most recent upstream HTTP response so SDK
// calls can still surface llm-proxy headers (X-LLM-*, X-RateLimit-*).
type capturingTransport struct {
	inner http.RoundTripper
	mu    sync.Mutex
	last  *http.Response
	body  []byte
}

func newCapturingTransport() *capturingTransport {
	return &capturingTransport{
		inner: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}
}

func (t *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = resp
	t.body = nil
	if resp != nil && resp.Body != nil {
		body, readErr := io.ReadAll(resp.Body)
		if readErr == nil {
			t.body = bytes.Clone(body)
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return resp, err
}

func (t *capturingTransport) snapshot() *http.Response {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

func (t *capturingTransport) snapshotBody() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return bytes.Clone(t.body)
}

func responseFromCapture(resp *http.Response) ProxyResponse {
	if resp == nil {
		return ProxyResponse{}
	}
	return ProxyResponse{
		Status:    resp.StatusCode,
		Headers:   resp.Header.Clone(),
		Trailer:   resp.Trailer.Clone(),
		InputTok:  resp.Header.Get("X-LLM-Input-Tokens"),
		OutputTok: resp.Header.Get("X-LLM-Output-Tokens"),
		Provider:  resp.Header.Get("X-LLM-Provider"),
		Model:     resp.Header.Get("X-LLM-Model"),
	}
}
