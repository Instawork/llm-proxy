package redact

import "testing"

func TestTierCounts(t *testing.T) {
	masked, sealed, redacted := TierCounts(map[string]int{
		"EMAIL_ADDRESS": 1,
		"US_SSN":        2,
		"CREDIT_CARD":   1,
	})
	if masked != 1 || sealed != 2 || redacted != 1 {
		t.Fatalf("TierCounts = (%d,%d,%d), want (1,2,1)", masked, sealed, redacted)
	}
	if got := TotalDetected(map[string]int{"EMAIL_ADDRESS": 1, "US_SSN": 2}); got != 3 {
		t.Fatalf("TotalDetected = %d, want 3", got)
	}
}
