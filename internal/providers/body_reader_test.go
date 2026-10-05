package providers

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, plain string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(plain)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestBodyReader_BytesTracksUnreadRemainder(t *testing.T) {
	src := []byte("hello world")
	r := NewBodyReader(src)

	if got := r.Bytes(); !bytes.Equal(got, src) {
		t.Fatalf("Bytes before read = %q, want %q", got, src)
	}
	if &r.Bytes()[0] != &src[0] {
		t.Fatal("Bytes must alias the caller's slice, not copy it")
	}

	head := make([]byte, 6)
	if _, err := io.ReadFull(r, head); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if got := r.Bytes(); string(got) != "world" {
		t.Fatalf("Bytes after partial read = %q, want %q", got, "world")
	}
	if _, err := io.ReadAll(r); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := r.Bytes(); len(got) != 0 {
		t.Fatalf("Bytes after full read = %q, want empty", got)
	}
}

func TestReadResponseBody_BorrowsFromBorrowersAndCopiesOtherwise(t *testing.T) {
	src := []byte(`{"ok":true}`)

	borrowed, err := readResponseBody(NewBodyReader(src))
	if err != nil {
		t.Fatalf("readResponseBody(BodyReader): %v", err)
	}
	if &borrowed[0] != &src[0] {
		t.Fatal("expected BodyReader contents to be borrowed, got a copy")
	}

	var buf bytes.Buffer
	buf.Write(src)
	fromBuf, err := readResponseBody(&buf)
	if err != nil {
		t.Fatalf("readResponseBody(bytes.Buffer): %v", err)
	}
	if !bytes.Equal(fromBuf, src) {
		t.Fatalf("bytes.Buffer contents = %q, want %q", fromBuf, src)
	}

	copied, err := readResponseBody(strings.NewReader(string(src)))
	if err != nil {
		t.Fatalf("readResponseBody(strings.Reader): %v", err)
	}
	if !bytes.Equal(copied, src) {
		t.Fatalf("strings.Reader contents = %q, want %q", copied, src)
	}
	if &copied[0] == &src[0] {
		t.Fatal("non-borrowable reader must produce an independent copy")
	}

	allocs := testing.AllocsPerRun(100, func() {
		_, _ = readResponseBody(NewBodyReader(src))
	})
	// One alloc for the BodyReader wrapper itself (and its embedded
	// bytes.Reader); the body is never copied.
	if allocs > 2 {
		t.Fatalf("readResponseBody over BodyReader allocated %.0f times per run, want <= 2", allocs)
	}
}

func TestDecompressResponseIfNeeded_BorrowedReaderFastPath(t *testing.T) {
	t.Run("plain body is returned unchanged and still borrowable", func(t *testing.T) {
		src := []byte(`data: {"choices":[]}` + "\n\n")
		br := NewBodyReader(src)
		out, err := DecompressResponseIfNeeded(br)
		if err != nil {
			t.Fatalf("DecompressResponseIfNeeded: %v", err)
		}
		if out != io.Reader(br) {
			t.Fatalf("expected the BodyReader itself back, got %T", out)
		}
		got, err := readResponseBody(out)
		if err != nil {
			t.Fatalf("readResponseBody: %v", err)
		}
		if &got[0] != &src[0] {
			t.Fatal("plain fast path must preserve zero-copy borrowing")
		}
	})

	t.Run("gzip body is inflated", func(t *testing.T) {
		const plain = `{"usage":{"total_tokens":3}}`
		out, err := DecompressResponseIfNeeded(NewBodyReader(gzipBytes(t, plain)))
		if err != nil {
			t.Fatalf("DecompressResponseIfNeeded: %v", err)
		}
		gz, ok := out.(*gzip.Reader)
		if !ok {
			t.Fatalf("expected *gzip.Reader, got %T", out)
		}
		defer gz.Close()
		got, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("ReadAll(gzip): %v", err)
		}
		if string(got) != plain {
			t.Fatalf("inflated = %q, want %q", got, plain)
		}
	})

	t.Run("generic reader keeps the peek path", func(t *testing.T) {
		const plain = `{"usage":{"total_tokens":3}}`
		for name, body := range map[string][]byte{"plain": []byte(plain), "gzip": gzipBytes(t, plain)} {
			out, err := DecompressResponseIfNeeded(iotestReader{bytes.NewReader(body)})
			if err != nil {
				t.Fatalf("%s: DecompressResponseIfNeeded: %v", name, err)
			}
			if gz, ok := out.(*gzip.Reader); ok {
				defer gz.Close()
			}
			got, err := io.ReadAll(out)
			if err != nil {
				t.Fatalf("%s: ReadAll: %v", name, err)
			}
			if string(got) != plain {
				t.Fatalf("%s: body = %q, want %q", name, got, plain)
			}
		}
	})
}

// iotestReader hides every method except Read so the generic
// DecompressResponseIfNeeded path is exercised.
type iotestReader struct{ r io.Reader }

func (w iotestReader) Read(p []byte) (int, error) { return w.r.Read(p) }

func TestParseOpenAIStreaming_BorrowedBodyIsNotMutated(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(`data: {"id":"chatcmpl-x","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}` + "\n\n")
	}
	b.WriteString(`data: {"id":"chatcmpl-x","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}` + "\n\n")
	b.WriteString("data: [DONE]\n\n")
	src := []byte(b.String())
	before := append([]byte(nil), src...)

	md, err := parseOpenAIFormatMetadata(NewBodyReader(src), true, "openai")
	if err != nil {
		t.Fatalf("parseOpenAIFormatMetadata: %v", err)
	}
	if md.TotalTokens != 9 || md.Model != "gpt-4o-mini" || md.RequestID != "chatcmpl-x" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
	if !bytes.Equal(src, before) {
		t.Fatal("parser mutated the borrowed capture buffer")
	}
}
