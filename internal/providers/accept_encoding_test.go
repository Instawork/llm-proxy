package providers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestGzipOnlyAcceptEncoding(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"gzip, deflate, br", "gzip"},
		{"gzip, deflate, br, zstd", "gzip"},
		{"br;q=1.0, identity;q=0.5, gzip;q=0.8", "identity;q=0.5, gzip;q=0.8"},
		{"X-GZIP, Br", "X-GZIP"},
		{"br", ""},
		{"zstd, deflate", ""},
		{"*", ""},
		{"", ""},
	} {
		if got := gzipOnlyAcceptEncoding(tc.in); got != tc.want {
			t.Errorf("gzipOnlyAcceptEncoding(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// upstreamAcceptEncoding sends one request with the given client
// Accept-Encoding through p's real rewrite and transport to a local upstream
// and returns the Accept-Encoding the upstream received.
func upstreamAcceptEncoding(t *testing.T, p interface {
	Provider
	WrapTransport(func(http.RoundTripper) http.RoundTripper)
}, path, clientAE string,
) string {
	t.Helper()
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- strings.Join(r.Header.Values("Accept-Encoding"), ", ")
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	p.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(req *http.Request) (*http.Response, error) {
			out := req.Clone(req.Context())
			out.URL.Scheme, out.URL.Host, out.Host = target.Scheme, target.Host, target.Host
			return inner.RoundTrip(out)
		})
	})
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261119/us-west-2/bedrock/aws4_request, SignedHeaders=accept-encoding;host;x-amz-date, Signature=0")
	if clientAE != "" {
		req.Header.Set("Accept-Encoding", clientAE)
	}
	p.Proxy().ServeHTTP(httptest.NewRecorder(), req)
	return <-seen
}

func TestCreateGenericRewrite_StripsBrotliFromAcceptEncoding(t *testing.T) {
	for _, tc := range []struct{ name, client, want string }{
		{"finch httpx: br and zstd stripped, gzip kept", "gzip, deflate, br, zstd", "gzip"},
		{"order and q-values kept", "identity;q=0.5, br, gzip;q=0.9", "identity;q=0.5, gzip;q=0.9"},
		{"br only: header removed, transport negotiates gzip", "br", "gzip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, p := range []interface {
				Provider
				WrapTransport(func(http.RoundTripper) http.RoundTripper)
			}{NewAnthropicProxy(), NewOpenAIProxy(), NewGeminiProxy(), NewOpenRouterProxy()} {
				if got := upstreamAcceptEncoding(t, p, "/"+p.GetName()+"/v1/messages", tc.client); got != tc.want {
					t.Errorf("%s: upstream Accept-Encoding = %q, want %q", p.GetName(), got, tc.want)
				}
			}
		})
	}
}

// Bedrock is a SigV4 passthrough: Accept-Encoding may be signed, so it must
// reach AWS exactly as the client sent it.
func TestBedrockRewrite_ForwardsAcceptEncodingVerbatim(t *testing.T) {
	got := upstreamAcceptEncoding(t, NewBedrockProxy(), "/bedrock/model/us.anthropic.claude-haiku-5-5/invoke", "gzip, deflate, br")
	if got != "gzip, deflate, br" {
		t.Errorf("upstream Accept-Encoding = %q, want the client's value verbatim", got)
	}
}
