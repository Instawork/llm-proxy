package apikeys

import "testing"

func TestEffectiveRedactPII(t *testing.T) {
	keyInherit := &APIKey{}
	keyOn := &APIKey{RedactPII: boolPtr(true)}
	keyOff := &APIKey{RedactPII: boolPtr(false)}

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
	keyOff := &APIKey{AllowStreaming: boolPtr(false)}
	keyOn := &APIKey{AllowStreaming: boolPtr(true)}

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

func boolPtr(v bool) *bool { return &v }
