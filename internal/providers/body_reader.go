package providers

import (
	"bytes"
	"io"
)

// SSE framing tokens shared by the byte-oriented stream parsers.
var (
	sseDataPrefix = []byte("data: ")
	sseDoneMarker = []byte("[DONE]")
)

// bytesBorrower is satisfied by readers that can lend their unread contents
// as a slice without copying — *bytes.Buffer and *BodyReader both qualify.
// Borrowed slices are read-only: they alias the caller's buffer.
type bytesBorrower interface {
	Bytes() []byte
}

// BodyReader is an io.Reader over an already-captured response body that
// also lends the unread remainder via Bytes. The token-parsing middleware
// hands the captured stream to Provider.ParseResponseMetadata through one
// of these so parsers that need the whole body (readResponseBody) borrow
// the capture buffer instead of re-copying it with io.ReadAll, while
// line-oriented parsers keep streaming through the io.Reader side.
type BodyReader struct {
	*bytes.Reader
	buf []byte
}

// NewBodyReader wraps b without copying it. The caller must not modify b
// while the reader is in use.
func NewBodyReader(b []byte) *BodyReader {
	return &BodyReader{Reader: bytes.NewReader(b), buf: b}
}

// Bytes returns the unread portion of the body, mirroring bytes.Buffer.Bytes.
func (r *BodyReader) Bytes() []byte {
	return r.buf[len(r.buf)-r.Reader.Len():]
}

// readResponseBody returns the full remaining contents of r. When r can
// lend its backing slice no copy is made; otherwise it falls back to
// io.ReadAll. The result must be treated as read-only.
func readResponseBody(r io.Reader) ([]byte, error) {
	if b, ok := r.(bytesBorrower); ok {
		return b.Bytes(), nil
	}
	return io.ReadAll(r)
}
