package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// countingReader records how many bytes were pulled from the underlying
// client stream so the test can prove the proxy did not slurp the whole
// upload before deciding it was oversize.
type countingReader struct {
	r    io.Reader
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

func TestReadBoundedBody_WithinCapBuffersAndSetsGetBody(t *testing.T) {
	payload := strings.Repeat("a", 100)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(payload))

	body, oversize, err := readBoundedBody(req, 1024)
	if err != nil || oversize {
		t.Fatalf("err=%v oversize=%v", err, oversize)
	}
	if string(body) != payload {
		t.Fatalf("body = %q", body)
	}
	if req.GetBody == nil {
		t.Fatal("GetBody not set for in-cap body")
	}
	rc, _ := req.GetBody()
	again, _ := io.ReadAll(rc)
	rest, _ := io.ReadAll(req.Body)
	if string(again) != payload || string(rest) != payload {
		t.Fatalf("GetBody=%q Body=%q", again, rest)
	}
}

func TestReadBoundedBody_OversizeStopsReadingButStreamsFullBodyUpstream(t *testing.T) {
	const maxBytes = 1024
	payload := strings.Repeat("x", 64*1024)
	cr := &countingReader{r: strings.NewReader(payload)}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", cr)
	req.ContentLength = int64(len(payload))

	body, oversize, err := readBoundedBody(req, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !oversize {
		t.Fatal("expected oversize")
	}
	if len(body) != maxBytes+1 {
		t.Fatalf("buffered %d bytes, want exactly maxBytes+1 = %d", len(body), maxBytes+1)
	}
	// Only the probe prefix was pulled from the client so far (allow for the
	// reader's internal chunking, but nowhere near the whole payload).
	if cr.read > 2*maxBytes+1 {
		t.Fatalf("read %d bytes from client before oversize decision; want <= %d", cr.read, 2*maxBytes+1)
	}
	if observedBodyBytes(req, body) != len(payload) {
		t.Fatalf("observedBodyBytes = %d, want declared Content-Length %d", observedBodyBytes(req, body), len(payload))
	}

	// Upstream must still receive every byte, in order, untruncated.
	upstream, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upstream, []byte(payload)) {
		t.Fatalf("upstream body corrupted: got %d bytes, want %d", len(upstream), len(payload))
	}
	if req.ContentLength != int64(len(payload)) {
		t.Fatalf("ContentLength changed to %d", req.ContentLength)
	}
}

func TestPIIRedactMiddleware_OversizeWireModeNeverTruncatesUpstream(t *testing.T) {
	payload := `{"model":"gpt-4o","messages":[{"role":"user","content":"` + strings.Repeat("y", 8*1024) + `"}]}`
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, WirePlaceholders: true, MaxBodyBytes: 1024})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", payload))

	if r.called != 0 {
		t.Fatalf("redactor must not run on oversize body (called=%d)", r.called)
	}
	if string(cap.bodySeen) != payload {
		t.Fatalf("upstream body truncated: got %d bytes, want %d", len(cap.bodySeen), len(payload))
	}
}
