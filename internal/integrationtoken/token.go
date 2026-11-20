// Package integrationtoken mints and parses the bearer tokens that
// integration callers (for example Init) present on
// /admin/api/integrations/*. Only the SHA-256 of a token is ever deployed;
// the plaintext lives in the caller's own secret store.
package integrationtoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Prefix starts every integration token. It must never be "sk-", so the
// proxy's key-auth middleware can't mistake a token for a proxy key.
const Prefix = "llmpi_"

var callerIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// ValidCallerID reports whether id is a valid integration caller id. The id
// becomes KeyRequest.Source and appears in tokens, tags, and audit events.
func ValidCallerID(id string) bool {
	return callerIDPattern.MatchString(id)
}

// New mints a token for callerID: "llmpi_<caller>_<base64url 32 random bytes>".
// It returns the plaintext and its hex SHA-256.
func New(callerID string) (token, sha256Hex string, err error) {
	if !ValidCallerID(callerID) {
		return "", "", fmt.Errorf("invalid caller id %q: must match %s", callerID, callerIDPattern)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token = Prefix + callerID + "_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := Hash(token)
	return token, hex.EncodeToString(sum[:]), nil
}

// Hash returns the SHA-256 of a token.
func Hash(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

// ParseHashes decodes a comma-separated list of hex SHA-256 digests. More
// than one digest lets a caller rotate tokens without a gap: deploy old and
// new hashes together, switch the caller, then drop the old hash.
func ParseHashes(value string) ([][32]byte, error) {
	var out [][32]byte
	for part := range strings.SplitSeq(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		b, err := hex.DecodeString(part)
		if err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("not a hex sha256 digest")
		}
		out = append(out, [32]byte(b))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no sha256 digest set")
	}
	return out, nil
}
