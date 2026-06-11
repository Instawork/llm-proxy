package redact

import (
	"strings"
	"testing"
)

func TestPolicyFor_KnownTiers(t *testing.T) {
	if PolicyFor("US_SSN") != PolicySeal {
		t.Fatal("US_SSN should be SEAL")
	}
	if PolicyFor("CREDIT_CARD") != PolicyRedact {
		t.Fatal("CREDIT_CARD should be REDACT")
	}
	if PolicyFor("PERSON") != PolicyMask {
		t.Fatal("PERSON should be MASK")
	}
	if PolicyFor("UNKNOWN_THING") != PolicyRedact {
		t.Fatal("unknown entity should default to REDACT")
	}
}

func TestRegistry_MaskRoundTrip(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	if ph != "<PERSON_1>" {
		t.Fatalf("placeholder = %q", ph)
	}
	again := reg.Placeholder("PERSON", "Jane Doe")
	if again != ph {
		t.Fatalf("expected stable placeholder, got %q", again)
	}
	in := "hello " + ph + "!"
	out := reg.RestoreUserFacing(in)
	if out != "hello Jane Doe!" {
		t.Fatalf("restore = %q", out)
	}
}

func TestRegistry_MaskRestoresJSONEscapedPlaceholders(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "alice@example.com")
	escaped := jsonEscapedPlaceholder(ph)
	in := `{"content":"` + escaped + `"}`
	out := reg.RestoreUserFacing(in)
	if !strings.Contains(out, "alice@example.com") {
		t.Fatalf("expected restored email in %q", out)
	}
	if strings.Contains(out, escaped) {
		t.Fatalf("escaped placeholder should be replaced: %q", out)
	}
}

func TestRegistry_SealDoesNotRestore(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("US_SSN", "222-33-4444")
	if ph != "<US_SSN_1>" {
		t.Fatalf("placeholder = %q", ph)
	}
	out := reg.RestoreUserFacing("ssn " + ph)
	if out != "ssn "+ph {
		t.Fatalf("SEAL must stay opaque, got %q", out)
	}
}

func TestRegistry_RedactMarker(t *testing.T) {
	reg := NewRegistry()
	m := reg.Placeholder("CREDIT_CARD", "4111-1111-1111-1111")
	if m != "[REDACTED:CREDIT_CARD]" {
		t.Fatalf("marker = %q", m)
	}
}

func TestRegistry_RestoreStreamChunk_SplitPlaceholder(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Alice")
	parts := []string{"prefix ", ph[:4], ph[4:] + " suffix"}
	var carry []byte
	var got strings.Builder
	for _, part := range parts {
		emit, newCarry := reg.RestoreStreamChunk([]byte(part), carry)
		carry = newCarry
		got.Write(emit)
	}
	if tail := reg.FlushCarry(carry); len(tail) > 0 {
		got.Write(tail)
	}
	want := "prefix Alice suffix"
	if got.String() != want {
		t.Fatalf("stream restore = %q want %q", got.String(), want)
	}
}

func TestScrub_PolicyAwarePlaceholders(t *testing.T) {
	spans := []Span{
		{Start: 0, End: 8, EntityType: "PERSON", Score: 0.9},
		{Start: 9, End: 20, EntityType: "US_SSN", Score: 0.9},
	}
	reg := NewRegistry()
	res := spliceSpans("Jane Doe 222-33-4444", spans, 0.5, reg, false)
	if !strings.Contains(res.Text, "<PERSON_1>") {
		t.Fatalf("expected PERSON placeholder in %q", res.Text)
	}
	if !strings.Contains(res.Text, "<US_SSN_1>") {
		t.Fatalf("expected US_SSN placeholder in %q", res.Text)
	}
	restored := reg.RestoreUserFacing(res.Text)
	if !strings.Contains(restored, "Jane Doe") {
		t.Fatalf("PERSON not restored: %q", restored)
	}
	if strings.Contains(restored, "222-33-4444") {
		t.Fatalf("US_SSN must not restore: %q", restored)
	}
}
