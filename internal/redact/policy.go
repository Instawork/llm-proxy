package redact

// Policy selects how a detected entity is replaced and whether the
// original value can be restored at the user-facing boundary.
type Policy int

const (
	// PolicyMask replaces with a numbered placeholder (<PII_PERSON_1>) that
	// round-trips back to the original value in responses to the client.
	PolicyMask Policy = iota
	// PolicySeal replaces with a numbered placeholder that stays opaque
	// to the client — the upstream LLM never sees the raw value, but the
	// user also never gets it back automatically.
	PolicySeal
	// PolicyRedact replaces with a fixed [REDACTED:TYPE] marker. No
	// registry entry is created and there is nothing to restore.
	PolicyRedact
)

// defaultPolicy mirrors the tier comments in recognizers.yaml.
var defaultPolicy = map[string]Policy{
	// SEAL — identity verification
	"US_SSN":            PolicySeal,
	"US_ITIN":           PolicySeal,
	"US_PASSPORT":       PolicySeal,
	"DATE_OF_BIRTH":     PolicySeal,
	"US_STREET_ADDRESS": PolicySeal,

	// REDACT — payment rails
	"CREDIT_CARD":    PolicyRedact,
	"US_BANK_NUMBER": PolicyRedact,
	"IBAN_CODE":      PolicyRedact,

	// MASK — quasi-identifiers
	"US_DRIVER_LICENSE": PolicyMask,
	"PERSON":            PolicyMask,
	"EMAIL_ADDRESS":     PolicyMask,
	"PHONE_NUMBER":      PolicyMask,
	"LOCATION":          PolicyMask,
	"IP_ADDRESS":        PolicyMask,
}

// PolicyFor returns the scrub policy for an entity type. Unknown types
// default to REDACT so we fail closed on unexpected recognizer output.
func PolicyFor(entityType string) Policy {
	if p, ok := defaultPolicy[entityType]; ok {
		return p
	}
	return PolicyRedact
}

func (p Policy) String() string {
	switch p {
	case PolicyMask:
		return "mask"
	case PolicySeal:
		return "seal"
	case PolicyRedact:
		return "redact"
	default:
		return "unknown"
	}
}
