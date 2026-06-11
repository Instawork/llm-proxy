package apikeys

import "testing"

func TestEffectiveRedactPII(t *testing.T) {
	trueVal := true
	falseVal := false
	keyOn := &APIKey{RedactPII: &trueVal}
	keyOff := &APIKey{RedactPII: &falseVal}
	keyInherit := &APIKey{}

	if !EffectiveRedactPII(true, keyInherit) {
		t.Fatal("global on + inherit should redact")
	}
	if EffectiveRedactPII(false, keyInherit) {
		t.Fatal("global off + inherit should not redact")
	}
	if !EffectiveRedactPII(false, keyOn) {
		t.Fatal("key override on should redact when global off")
	}
	if EffectiveRedactPII(true, keyOff) {
		t.Fatal("key override off should skip when global on")
	}
}
