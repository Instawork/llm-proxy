package apikeys

import "testing"

func TestEffectiveRedactPII(t *testing.T) {
	keyInherit := &APIKey{}
	keyOn := &APIKey{RedactPII: new(true)}
	keyOff := &APIKey{RedactPII: new(false)}

	if !EffectiveRedactPII(true, keyInherit) {
		t.Fatal("inherit true")
	}
	if EffectiveRedactPII(false, keyInherit) {
		t.Fatal("inherit false")
	}
	if !EffectiveRedactPII(false, keyOn) {
		t.Fatal("key on")
	}
	if EffectiveRedactPII(true, keyOff) {
		t.Fatal("key off")
	}
}

func TestEffectiveAllowStreaming(t *testing.T) {
	keyInherit := &APIKey{}
	keyOff := &APIKey{AllowStreaming: new(false)}
	keyOn := &APIKey{AllowStreaming: new(true)}

	if !EffectiveAllowStreaming(true, keyInherit) {
		t.Fatal("inherit true")
	}
	if EffectiveAllowStreaming(false, keyInherit) {
		t.Fatal("inherit false")
	}
	if EffectiveAllowStreaming(true, keyOff) {
		t.Fatal("key off")
	}
	if !EffectiveAllowStreaming(false, keyOn) {
		t.Fatal("key on")
	}
}

//go:fix inline
func boolPtr(v bool) *bool { return new(v) }
