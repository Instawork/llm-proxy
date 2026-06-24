package redact

// TierCounts sums entity detection counts by scrub policy tier.
func TierCounts(entityCounts map[string]int) (masked, sealed, redacted int) {
	for entityType, n := range entityCounts {
		switch PolicyFor(entityType) {
		case PolicyMask:
			masked += n
		case PolicySeal:
			sealed += n
		default:
			redacted += n
		}
	}
	return masked, sealed, redacted
}

// TotalDetected returns the sum of all entity counts.
func TotalDetected(entityCounts map[string]int) int {
	n := 0
	for _, c := range entityCounts {
		n += c
	}
	return n
}
