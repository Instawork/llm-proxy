package cloudflare

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBearerSkIWWirefilter_MatchesProxyKeys(t *testing.T) {
	re := regexp.MustCompile(BearerSkIWWirefilter)

	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "generated", header: "Bearer sk-iw-" + strings.Repeat("a", 64), want: true},
		{name: "lowercase-bearer", header: "bearer sk-iw-deadbeef", want: true},
		{name: "empty-suffix", header: "Bearer sk-iw-", want: true},
		{name: "upstream-openai", header: "Bearer sk-proj-upstream", want: false},
		{name: "token-not-bearer", header: "Token sk-iw-should-not-match", want: false},
		{name: "legacy-iw-colon", header: "iw:legacy-key", want: false},
		{name: "empty", header: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := re.MatchString(tc.header)
			require.Equal(t, tc.want, got, "wirefilter regex mismatch")
			assert.Equal(t, tc.want, MatchesBearerSkIW(tc.header))
		})
	}
}
