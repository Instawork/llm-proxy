package apikeys

import "testing"

func TestEffectiveRedactPII(t *testing.T) {
	keyInherit := &APIKey{}
	keyOn := &APIKey{RedactPII: new(true)}
	keyOff := &APIKey{RedactPII: new(false)}

	cases := []struct {
		name                string
		globalEnabled       bool
		allowPerKeyOverride bool
		key                 *APIKey
		want                bool
	}{
		// allowPerKeyOverride = false: always enforce globalEnabled
		{"override off: global on, nil key", true, false, nil, true},
		{"override off: global on, inherit key", true, false, keyInherit, true},
		{"override off: global on, key on", true, false, keyOn, true},
		{"override off: global on, key off ignored", true, false, keyOff, true},
		{"override off: global off, nil key", false, false, nil, false},
		{"override off: global off, inherit key", false, false, keyInherit, false},
		{"override off: global off, key on ignored", false, false, keyOn, false},
		{"override off: global off, key off", false, false, keyOff, false},

		// allowPerKeyOverride = true: honor explicit key.RedactPII
		{"override on: global on, nil key", true, true, nil, true},
		{"override on: global on, inherit key", true, true, keyInherit, true},
		{"override on: global on, key on", true, true, keyOn, true},
		{"override on: global on, key off honored", true, true, keyOff, false},
		{"override on: global off, nil key", false, true, nil, false},
		{"override on: global off, inherit key", false, true, keyInherit, false},
		{"override on: global off, key on honored", false, true, keyOn, true},
		{"override on: global off, key off", false, true, keyOff, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveRedactPII(tc.globalEnabled, tc.allowPerKeyOverride, tc.key)
			if got != tc.want {
				t.Fatalf("EffectiveRedactPII(%v, %v, %+v) = %v, want %v",
					tc.globalEnabled, tc.allowPerKeyOverride, tc.key, got, tc.want)
			}
		})
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
