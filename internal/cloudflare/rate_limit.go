package cloudflare

import (
	"regexp"
	"strings"
)

// BearerSkIWWirefilter is the Cloudflare Wirefilter regex that matches the
// proxy's Bearer sk-iw-* API keys. It mirrors the expression used by the edge
// WAF rate-limit rule so the proxy and the edge agree on what counts as a
// proxy key (keep the two in sync if either changes).
const BearerSkIWWirefilter = `[Bb]earer sk-iw-.*`

var bearerSkIWHeader = regexp.MustCompile(`(?i)^Bearer sk-iw-.*`)

// MatchesBearerSkIW reports whether an Authorization header value is a Bearer
// sk-iw-* llm-proxy key (the shape Cloudflare rate-limit rules key on).
func MatchesBearerSkIW(authorizationHeader string) bool {
	return bearerSkIWHeader.MatchString(strings.TrimSpace(authorizationHeader))
}
