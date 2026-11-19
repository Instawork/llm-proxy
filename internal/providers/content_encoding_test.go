package providers

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestNarrowAcceptEncoding(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"gzip, deflate, br", "gzip, deflate, br"},
		{"gzip, deflate, br, zstd", "gzip, deflate, br"},
		{"br;q=1.0, zstd;q=0.9, gzip;q=0.5", "br;q=1.0, gzip;q=0.5"},
		{"zstd, br", "br"},
		{"GZIP;Q=0.8,Br", "GZIP;Q=0.8, Br"},
		{"identity", "identity"},
		{"x-gzip, compress", "x-gzip"},
		{"zstd", ""},
		{"*", ""},
		{"*;q=0, gzip", "*;q=0, gzip"},
		{"*;q=0.5, gzip", "gzip"},
		{"", ""},
		{" , ,gzip", "gzip"},
	} {
		if got := NarrowAcceptEncoding(tc.in); got != tc.want {
			t.Errorf("NarrowAcceptEncoding(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func encodeWith(t *testing.T, coding string, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch coding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "zlib":
		w = zlib.NewWriter(&buf)
	case "flate":
		fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
		if err != nil {
			t.Fatal(err)
		}
		w = fw
	case "br":
		w = brotli.NewWriterLevel(&buf, 5)
	default:
		t.Fatalf("unknown coding %q", coding)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecodeContentEncoding(t *testing.T) {
	plain := []byte(`{"usage":{"input_tokens":250000,"output_tokens":170}}`)

	for _, tc := range []struct {
		name, header, coding string
	}{
		{"gzip", "gzip", "gzip"},
		{"x-gzip", "x-gzip", "gzip"},
		{"deflate zlib-wrapped", "deflate", "zlib"},
		{"deflate raw", "deflate", "flate"},
		{"br", "br", "br"},
		{"br uppercase", " BR ", "br"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeContentEncoding(encodeWith(t, tc.coding, plain), tc.header, 1<<20)
			if err != nil || !bytes.Equal(got, plain) {
				t.Fatalf("got %q, %v; want %q", got, err, plain)
			}
		})
	}

	t.Run("stacked codings decode in reverse order", func(t *testing.T) {
		wire := encodeWith(t, "br", encodeWith(t, "gzip", plain))
		got, err := DecodeContentEncoding(wire, "gzip, br", 1<<20)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("identity and empty return body without copying", func(t *testing.T) {
		for _, header := range []string{"", "identity"} {
			got, err := DecodeContentEncoding(plain, header, 1)
			if err != nil || &got[0] != &plain[0] {
				t.Fatalf("header %q: err %v, copied=%v", header, err, &got[0] != &plain[0])
			}
		}
	})

	t.Run("unsupported coding", func(t *testing.T) {
		_, err := DecodeContentEncoding(plain, "zstd", 1<<20)
		if !errors.Is(err, ErrUnsupportedContentEncoding) {
			t.Fatalf("err = %v, want ErrUnsupportedContentEncoding", err)
		}
	})

	t.Run("decoded size is bounded", func(t *testing.T) {
		bomb := encodeWith(t, "br", make([]byte, 1<<20))
		_, err := DecodeContentEncoding(bomb, "br", 64<<10)
		if !errors.Is(err, ErrDecodedBodyTooLarge) {
			t.Fatalf("err = %v, want ErrDecodedBodyTooLarge", err)
		}
		if got, err := DecodeContentEncoding(bomb, "br", 1<<20); err != nil || len(got) != 1<<20 {
			t.Fatalf("exactly-at-limit body: %d bytes, %v", len(got), err)
		}
	})

	t.Run("truncated input returns partial output and an error", func(t *testing.T) {
		long := bytes.Repeat([]byte(`data: {"type":"ping"}`+"\n\n"), 2000)
		wire := encodeWith(t, "br", long)
		got, err := DecodeContentEncoding(wire[:len(wire)-4], "br", 1<<20)
		if err == nil || len(got) == 0 || !bytes.HasPrefix(long, got) {
			t.Fatalf("got %d bytes, err %v; want a non-empty prefix and an error", len(got), err)
		}
	})
}

type acceptEncodingUpstream struct {
	mu   sync.Mutex
	seen []string
	url  *url.URL
}

func newAcceptEncodingUpstream(t *testing.T) *acceptEncodingUpstream {
	t.Helper()
	u := &acceptEncodingUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, strings.Join(r.Header.Values("Accept-Encoding"), ", "))
		u.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	u.url, _ = url.Parse(srv.URL)
	return u
}

func (u *acceptEncodingUpstream) last() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seen[len(u.seen)-1]
}

func (u *acceptEncodingUpstream) redirect(inner http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		out := req.Clone(req.Context())
		out.URL.Scheme, out.URL.Host, out.Host = u.url.Scheme, u.url.Host, u.url.Host
		return inner.RoundTrip(out)
	})
}

// TestCreateGenericRewrite_NarrowsAcceptEncoding exercises the real provider
// rewrite and transport: unsupported codings never reach the upstream.
func TestCreateGenericRewrite_NarrowsAcceptEncoding(t *testing.T) {
	for _, tc := range []struct {
		name        string
		disableGzip bool
		client      []string
		want        string
	}{
		{"finch httpx", false, []string{"gzip, deflate, br, zstd"}, "gzip, deflate, br"},
		{"order and q-values kept", false, []string{"zstd;q=1.0, br;q=0.9, gzip;q=0.1"}, "br;q=0.9, gzip;q=0.1"},
		{"multiple header lines", false, []string{"zstd", "br"}, "br"},
		{"nothing decodable falls back to transport gzip", false, []string{"zstd"}, "gzip"},
		{"absent stays transport gzip", false, nil, "gzip"},
		{"disable-gzip strips", true, []string{"gzip, br"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newAcceptEncodingUpstream(t)
			for _, p := range []interface {
				Provider
				WrapTransport(func(http.RoundTripper) http.RoundTripper)
			}{
				NewAnthropicProxy(ProxyOptions{DisableGzip: tc.disableGzip}),
				NewOpenAIProxy(ProxyOptions{DisableGzip: tc.disableGzip}),
				NewGeminiProxy(ProxyOptions{DisableGzip: tc.disableGzip}),
				NewOpenRouterProxy(ProxyOptions{DisableGzip: tc.disableGzip}),
			} {
				p.WrapTransport(upstream.redirect)
				req := httptest.NewRequest(http.MethodGet, "/"+p.GetName()+"/v1/models", nil)
				for _, v := range tc.client {
					req.Header.Add("Accept-Encoding", v)
				}
				p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
				if got := upstream.last(); got != tc.want {
					t.Errorf("%s: upstream Accept-Encoding = %q, want %q", p.GetName(), got, tc.want)
				}
			}
		})
	}
}

// TestBedrockRewrite_LeavesAcceptEncodingUntouched guards the SigV4
// passthrough: Accept-Encoding may be a signed header, so it is forwarded
// exactly as the client sent it.
func TestBedrockRewrite_LeavesAcceptEncodingUntouched(t *testing.T) {
	upstream := newAcceptEncodingUpstream(t)
	p := NewBedrockProxy()
	p.WrapTransport(upstream.redirect)
	req := httptest.NewRequest(http.MethodPost, "/bedrock/model/us.anthropic.claude-haiku-5-5/invoke", strings.NewReader("{}"))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261119/us-west-2/bedrock/aws4_request, SignedHeaders=accept-encoding;host;x-amz-date, Signature=0")
	req.Header.Set("Accept-Encoding", "gzip, zstd")
	p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
	if got := upstream.last(); got != "gzip, zstd" {
		t.Errorf("upstream Accept-Encoding = %q, want the client's value verbatim", got)
	}
}
