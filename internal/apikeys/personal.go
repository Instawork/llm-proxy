package apikeys

import "strings"

// IsPersonalKey reports whether k is a viewer-owned personal proxy key.
func IsPersonalKey(k *APIKey) bool {
	if k == nil {
		return false
	}
	if k.Tags != nil && k.Tags["personal"] == "true" {
		return true
	}
	return strings.TrimSpace(k.OwnerEmail) != ""
}
