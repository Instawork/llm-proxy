package apikeys

import (
	"fmt"
	"strings"
	"sync"
)

const BedrockProvider = "bedrock"

// defaultPIIOffNonBedrockBypassAdmins is the built-in allowlist used when the
// deployment config does not override it (see SetPIIOffNonBedrockBypassAdmins).
//
// It ships EMPTY on purpose: the bypass lets an admin create keys that send
// un-redacted PII to a non-Bedrock provider, so the safe default is that
// nobody can. Operators opt specific admins in via
// features.admin_dashboard.pii_off_bypass_admins in YAML, which keeps the
// roster out of source control and avoids a code deploy on every change.
var defaultPIIOffNonBedrockBypassAdmins = []string{}

var (
	piiOffBypassMu     sync.RWMutex
	piiOffBypassAdmins = normalizeBypassAdmins(defaultPIIOffNonBedrockBypassAdmins)
)

// normalizeBypassAdmins lowercases/trims entries and drops blanks, returning a
// set for O(1) lookup.
func normalizeBypassAdmins(emails []string) map[string]struct{} {
	out := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		e = strings.ToLower(strings.TrimSpace(e))
		if e != "" {
			out[e] = struct{}{}
		}
	}
	return out
}

// SetPIIOffNonBedrockBypassAdmins replaces the allowlist at startup from
// config. An empty/nil list falls back to the built-in default
// (defaultPIIOffNonBedrockBypassAdmins), which ships empty — so an
// unconfigured deployment allows no PII-off bypass at all. Safe for
// concurrent use.
func SetPIIOffNonBedrockBypassAdmins(emails []string) {
	normalized := normalizeBypassAdmins(emails)
	if len(normalized) == 0 {
		normalized = normalizeBypassAdmins(defaultPIIOffNonBedrockBypassAdmins)
	}
	piiOffBypassMu.Lock()
	piiOffBypassAdmins = normalized
	piiOffBypassMu.Unlock()
}

// CanBypassPIIOffNonBedrockPolicy reports whether an admin may create or update
// keys with PII redaction disabled on a non-Bedrock provider.
func CanBypassPIIOffNonBedrockPolicy(email string) bool {
	piiOffBypassMu.RLock()
	defer piiOffBypassMu.RUnlock()
	_, ok := piiOffBypassAdmins[strings.ToLower(strings.TrimSpace(email))]
	return ok
}

// ShouldEnforceBedrockForPIIOff returns true when the key must use Bedrock
// because PII redaction is effectively disabled due to an explicit per-key off
// or inheriting while global PII is enabled.
func ShouldEnforceBedrockForPIIOff(globalEnabled bool, key *APIKey) bool {
	if EffectiveRedactPII(globalEnabled, key) {
		return false
	}
	if key != nil && key.RedactPII != nil {
		return true
	}
	return globalEnabled
}

// ValidatePIIOffBedrockPolicy rejects non-Bedrock providers when PII
// redaction is off under the policy above. adminBypass skips the check for
// allowlisted admins in the dashboard.
func ValidatePIIOffBedrockPolicy(globalEnabled bool, provider string, redactPII *bool, adminBypass bool) error {
	if adminBypass {
		return nil
	}
	key := &APIKey{Provider: provider, RedactPII: redactPII}
	return enforcePIIOffBedrockProvider(globalEnabled, key)
}

// EnforcePIIOffBedrockProvider rejects requests for keys that violate the
// PII-off Bedrock policy. Used at request time (no admin bypass).
func EnforcePIIOffBedrockProvider(globalEnabled bool, key *APIKey) error {
	return enforcePIIOffBedrockProvider(globalEnabled, key)
}

func enforcePIIOffBedrockProvider(globalEnabled bool, key *APIKey) error {
	if !ShouldEnforceBedrockForPIIOff(globalEnabled, key) {
		return nil
	}
	if key != nil && strings.EqualFold(key.Provider, BedrockProvider) {
		return nil
	}
	return fmt.Errorf("keys with PII redaction disabled must use the bedrock provider")
}
