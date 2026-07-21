package providers

import (
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/require"
)

func TestEffectiveResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultResponseHeaderTimeout, effectiveResponseHeaderTimeout(0))
	require.Equal(t, DefaultResponseHeaderTimeout, effectiveResponseHeaderTimeout(-time.Second))
	require.Equal(t, 90*time.Second, effectiveResponseHeaderTimeout(90*time.Second))
}

func TestNewProxyTransportResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	tr := newProxyTransport(false, 0)
	require.Equal(t, DefaultResponseHeaderTimeout, tr.ResponseHeaderTimeout)
	require.False(t, tr.DisableCompression)

	tr = newProxyTransport(true, 2*time.Minute)
	require.Equal(t, 2*time.Minute, tr.ResponseHeaderTimeout)
	require.True(t, tr.DisableCompression)
}

func TestProviderConstructorsHonorResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	want := 7 * time.Minute
	opt := ProxyOptions{ResponseHeaderTimeout: want}

	cases := []struct {
		name string
		get  func(t *testing.T) *http.Transport
	}{
		{"openai", func(t *testing.T) *http.Transport {
			tr, ok := NewOpenAIProxy(opt).proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"anthropic", func(t *testing.T) *http.Transport {
			tr, ok := NewAnthropicProxy(opt).proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"gemini", func(t *testing.T) *http.Transport {
			tr, ok := NewGeminiProxy(opt).proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"bedrock", func(t *testing.T) *http.Transport {
			tr, ok := NewBedrockProxy(opt).proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"bedrock-mantle", func(t *testing.T) *http.Transport {
			proxy := newBedrockMantleProxy(
				"us-west-2",
				credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
				opt,
			)
			signer, ok := proxy.proxy.Transport.(*sigV4Transport)
			require.True(t, ok)
			tr, ok := signer.inner.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := tc.get(t)
			require.Equal(t, want, tr.ResponseHeaderTimeout)
		})
	}
}

func TestProviderConstructorsDefaultResponseHeaderTimeout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		get  func(t *testing.T) *http.Transport
	}{
		{"openai", func(t *testing.T) *http.Transport {
			tr, ok := NewOpenAIProxy().proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"anthropic", func(t *testing.T) *http.Transport {
			tr, ok := NewAnthropicProxy().proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"gemini", func(t *testing.T) *http.Transport {
			tr, ok := NewGeminiProxy().proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"bedrock", func(t *testing.T) *http.Transport {
			tr, ok := NewBedrockProxy().proxy.Transport.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
		{"bedrock-mantle", func(t *testing.T) *http.Transport {
			proxy := newBedrockMantleProxy(
				"us-west-2",
				credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
				ProxyOptions{},
			)
			signer, ok := proxy.proxy.Transport.(*sigV4Transport)
			require.True(t, ok)
			tr, ok := signer.inner.(*http.Transport)
			require.True(t, ok)
			return tr
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := tc.get(t)
			require.Equal(t, DefaultResponseHeaderTimeout, tr.ResponseHeaderTimeout)
		})
	}
}

func TestProxyOptionsDisableGzipIndependentOfTimeout(t *testing.T) {
	t.Parallel()

	opt := ProxyOptions{
		DisableGzip:           true,
		ResponseHeaderTimeout: 90 * time.Second,
	}
	tr, ok := NewOpenAIProxy(opt).proxy.Transport.(*http.Transport)
	require.True(t, ok)
	require.True(t, tr.DisableCompression)
	require.Equal(t, 90*time.Second, tr.ResponseHeaderTimeout)
}
