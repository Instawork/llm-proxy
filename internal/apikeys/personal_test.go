package apikeys

import "testing"

func TestIsPersonalKey(t *testing.T) {
	t.Run("tag", func(t *testing.T) {
		if !IsPersonalKey(&APIKey{Tags: map[string]string{"personal": "true"}}) {
			t.Fatal("expected personal tag")
		}
	})
	t.Run("owner email", func(t *testing.T) {
		if !IsPersonalKey(&APIKey{OwnerEmail: "viewer@example.com"}) {
			t.Fatal("expected owner email")
		}
	})
	t.Run("org key", func(t *testing.T) {
		if IsPersonalKey(&APIKey{DailyCostLimit: 1000}) {
			t.Fatal("expected org key")
		}
	})
}
