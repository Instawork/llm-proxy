package apikeys

import (
	"fmt"
	"hash/fnv"
	"strings"
)

const maskedIDSplit = "…"

var byoPrefixProviders = []struct {
	prefix   string
	provider string
}{
	{"sk-ant-", "anthropic"},
	{"sk-proj-", "openai"},
	{"sk-svcacct-", "openai"},
	{"sk-or-", "openai"},
	{"sk-", "openai"},
	{"AIza", "gemini"},
}

func isSupportedProxyProvider(provider string) bool {
	switch normalizeProvider(provider) {
	case "openai", "anthropic", "gemini", "bedrock", "bedrock-mantle":
		return true
	default:
		return false
	}
}

// ValidateBYOBanRequest checks that a ban targets a BYO masked id under a
// supported proxy route provider. When the masked id prefix implies a single
// provider (sk-ant- → anthropic), provider must match. Route-ambiguous
// prefixes (gsk_, xai-) require an explicit provider from request context.
func ValidateBYOBanRequest(provider, maskedID string) (string, error) {
	maskedID = strings.TrimSpace(maskedID)
	if !IsBYOMaskedKeyID(maskedID) {
		return "", fmt.Errorf("not a BYO masked key id")
	}
	provider = normalizeProvider(provider)
	if provider == "" {
		return "", fmt.Errorf("provider is required")
	}
	if !isSupportedProxyProvider(provider) {
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	if inferred := InferProviderFromMaskedID(maskedID); inferred != "" && inferred != provider {
		return "", fmt.Errorf("provider %q does not match masked key prefix (expected %q)", provider, inferred)
	}
	if _, err := ParseCredentialHashFromMaskedID(maskedID); err != nil {
		return "", err
	}
	return provider, nil
}

// MaskedIDHead returns the visible prefix portion of a masked key id.
func MaskedIDHead(maskedID string) string {
	maskedID = strings.TrimSpace(maskedID)
	if maskedID == "" {
		return ""
	}
	if idx := strings.LastIndex(maskedID, maskedIDSplit); idx >= 0 {
		return maskedID[:idx]
	}
	if idx := strings.LastIndex(maskedID, "..."); idx >= 0 {
		return maskedID[:idx]
	}
	return maskedID
}

// IsProxyMaskedKeyID reports whether maskedID identifies a registered proxy key.
func IsProxyMaskedKeyID(maskedID string) bool {
	head := MaskedIDHead(maskedID)
	if head == "" {
		return false
	}
	if HasKeyPrefix(head) {
		return true
	}
	return strings.HasPrefix(head, "sk-iw") || strings.HasPrefix(head, "iw:")
}

// IsBYOMaskedKeyID reports whether maskedID identifies a bring-your-own credential.
func IsBYOMaskedKeyID(maskedID string) bool {
	maskedID = strings.TrimSpace(maskedID)
	if maskedID == "" {
		return false
	}
	if !strings.Contains(maskedID, maskedIDSplit) && !strings.Contains(maskedID, "...") {
		return false
	}
	return !IsProxyMaskedKeyID(maskedID)
}

// InferProviderFromMaskedID guesses the provider from a BYO masked credential id.
func InferProviderFromMaskedID(maskedID string) string {
	head := MaskedIDHead(maskedID)
	for _, row := range byoPrefixProviders {
		if strings.HasPrefix(head, row.prefix) {
			return row.provider
		}
	}
	return ""
}

// BYOKeyLookup returns the stable registry key provider:hash for a masked BYO id.
func BYOKeyLookup(provider, maskedID string) (string, string, error) {
	if !IsBYOMaskedKeyID(maskedID) {
		return "", "", fmt.Errorf("not a BYO masked key id")
	}
	hash, err := ParseCredentialHashFromMaskedID(maskedID)
	if err != nil {
		return "", "", err
	}
	provider = normalizeProvider(provider)
	if provider == "" {
		provider = InferProviderFromMaskedID(maskedID)
	}
	if provider == "" {
		return "", "", fmt.Errorf("unknown BYO provider")
	}
	return provider + ":" + hash, provider, nil
}

// CredentialHashSuffix returns the 8-char lowercase hex FNV-1a/32 digest of a
// raw provider credential. Matches the suffix used by MaskProviderCredential
// in the request middleware.
func CredentialHashSuffix(raw string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.TrimSpace(raw)))
	return fmt.Sprintf("%08x", h.Sum32())
}

// ParseCredentialHashFromMaskedID extracts the hash suffix from a masked BYO
// credential id (e.g. "sk-ant-…a1b2c3d4").
func ParseCredentialHashFromMaskedID(maskedID string) (string, error) {
	maskedID = strings.TrimSpace(maskedID)
	if maskedID == "" {
		return "", fmt.Errorf("empty masked credential id")
	}
	var hash string
	if sep := strings.LastIndex(maskedID, "…"); sep >= 0 {
		hash = maskedID[sep+len("…"):]
	} else if sep := strings.LastIndex(maskedID, "..."); sep >= 0 {
		hash = maskedID[sep+3:]
	} else {
		return "", fmt.Errorf("invalid masked credential id")
	}
	if len(hash) != 8 {
		return "", fmt.Errorf("invalid masked credential hash")
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("invalid masked credential hash")
		}
	}
	return hash, nil
}
