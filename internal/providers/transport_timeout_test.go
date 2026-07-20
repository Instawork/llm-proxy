package providers

import (
	"net/http"
	"testing"
	"time"
)

func TestNewProxyTransportResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	tr := newProxyTransport(false, 0)
	if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("zero timeout: got %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}

	tr = newProxyTransport(false, 2*time.Minute)
	if tr.ResponseHeaderTimeout != 2*time.Minute {
		t.Fatalf("explicit timeout: got %v, want 2m", tr.ResponseHeaderTimeout)
	}
}

func TestProviderConstructorsHonorResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	want := 7 * time.Minute
	opt := ProxyOptions{ResponseHeaderTimeout: want}

	cases := []struct {
		name string
		get  func() *http.Transport
	}{
		{"openai", func() *http.Transport {
			return NewOpenAIProxy(opt).proxy.Transport.(*http.Transport)
		}},
		{"anthropic", func() *http.Transport {
			return NewAnthropicProxy(opt).proxy.Transport.(*http.Transport)
		}},
		{"gemini", func() *http.Transport {
			return NewGeminiProxy(opt).proxy.Transport.(*http.Transport)
		}},
		{"bedrock", func() *http.Transport {
			return NewBedrockProxy(opt).proxy.Transport.(*http.Transport)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := tc.get()
			if tr.ResponseHeaderTimeout != want {
				t.Fatalf("got %v, want %v", tr.ResponseHeaderTimeout, want)
			}
		})
	}
}

func TestProviderConstructorsDefaultResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	tr := NewOpenAIProxy().proxy.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("got %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	tr = NewGeminiProxy().proxy.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Fatalf("gemini default got %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
}
