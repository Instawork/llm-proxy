package redact

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
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
	if PolicyFor("US_DRIVER_LICENSE") != PolicyMask {
		t.Fatal("US_DRIVER_LICENSE should be MASK")
	}
	if PolicyFor("UNKNOWN_THING") != PolicyRedact {
		t.Fatal("unknown entity should default to REDACT")
	}
}

func TestRegistry_MaskRoundTrip(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	if ph != "<PII_PERSON_1>" {
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

func TestRegistry_MaskRoundTripNonASCII(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "José")
	out := reg.RestoreUserFacing("hello " + ph + "!")
	if out != "hello José!" {
		t.Fatalf("restore = %q", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("restore produced invalid UTF-8: %q", out)
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

func TestRegistry_MaskRestoresHTMLEscapedPlaceholders(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "bob@example.com")
	escaped := htmlEscapedPlaceholder(ph)
	in := `<p>` + escaped + `</p>`
	out := reg.RestoreUserFacing(in)
	if !strings.Contains(out, "bob@example.com") {
		t.Fatalf("expected restored email in %q", out)
	}
	if strings.Contains(out, escaped) {
		t.Fatalf("html-escaped placeholder should be replaced: %q", out)
	}
}

func TestRegistry_MaskPlaceholdersRemaining(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "a@b.com")
	if got := reg.MaskPlaceholdersRemaining("reply: " + ph); got != 1 {
		t.Fatalf("remaining = %d, want 1", got)
	}
	reg.RestoreUserFacing("reply: " + ph)
	if got := reg.MaskPlaceholdersRemaining("reply: a@b.com"); got != 0 {
		t.Fatalf("remaining after restore = %d, want 0", got)
	}
}

func TestRegistry_RestoreStreamChunk_EnforcesMaxCarry(t *testing.T) {
	reg := NewRegistry()
	garbage := bytes.Repeat([]byte("x"), maxPlaceholderCarry+10)
	garbage[0] = '<'
	emit, newCarry := reg.RestoreStreamChunk(garbage, nil)
	if len(newCarry) != 0 {
		t.Fatalf("expected carry flushed after max, got %d bytes", len(newCarry))
	}
	if len(emit) == 0 {
		t.Fatal("expected forced emit of oversized carry")
	}
}

func TestRegistry_SealDoesNotRestore(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("US_SSN", "222-33-4444")
	if ph != "<PII_US_SSN_1>" {
		t.Fatalf("placeholder = %q", ph)
	}
	out := reg.RestoreUserFacing("ssn " + ph)
	if out != "ssn "+ph {
		t.Fatalf("SEAL must stay opaque, got %q", out)
	}
}

func TestRegistry_RestoredCount(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "a@b.com")
	out := reg.RestoreUserFacing("reply: " + ph)
	if out != "reply: a@b.com" {
		t.Fatalf("restore failed: %q", out)
	}
	if got := reg.RestoredCount(); got != 1 {
		t.Fatalf("RestoredCount = %d, want 1", got)
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

// streamRestore pushes parts through RestoreStreamChunk + FlushCarry and
// returns the concatenated client-visible output.
func streamRestore(reg *Registry, parts []string) string {
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
	return got.String()
}

// TestRegistry_RestoreStreamChunk_SplitAtDelimiter guards the empty-suffix
// hold-back: a chunk ending exactly at the opening "<" used to be flushed,
// after which the reassembled token could never match a wire form and the
// client saw the mask token instead of the restored value.
func TestRegistry_RestoreStreamChunk_SplitAtDelimiter(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Alice") // "<PII_PERSON_1>"
	parts := []string{"prefix " + ph[:1], ph[1:] + " suffix"}
	if got := streamRestore(reg, parts); got != "prefix Alice suffix" {
		t.Fatalf("stream restore = %q want %q", got, "prefix Alice suffix")
	}
}

// TestRegistry_RestoreStreamChunk_EscapedFormSplit guards hold-back for the
// escaped wire forms: Gemini's JSON encoder emits \u003cPII_..._1\u003e, and a
// chunk boundary before the closing escape used to flush the token
// unrestored.
func TestRegistry_RestoreStreamChunk_EscapedFormSplit(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Alice")
	escaped := jsonEscapedPlaceholder(ph) // \u003cPII_PERSON_1\u003e

	// Split right before the closing escape.
	cut := strings.LastIndex(escaped, `\u003e`)
	parts := []string{"x " + escaped[:cut], escaped[cut:] + " y"}
	if got := streamRestore(reg, parts); got != "x Alice y" {
		t.Fatalf("escaped-form stream restore = %q want %q", got, "x Alice y")
	}

	// Split mid-escape (chunk ends "...\u00").
	reg2 := NewRegistry()
	ph2 := reg2.Placeholder("PERSON", "Bob")
	escaped2 := jsonEscapedPlaceholder(ph2)
	cut2 := strings.LastIndex(escaped2, `\u003e`) + 3 // inside the escape
	parts2 := []string{"x " + escaped2[:cut2], escaped2[cut2:] + " y"}
	if got := streamRestore(reg2, parts2); got != "x Bob y" {
		t.Fatalf("mid-escape stream restore = %q want %q", got, "x Bob y")
	}
}

// TestRegistry_RestoreUserFacing_QuoteBreaksRawJSON documents the bug: the
// verbatim restore splices an original containing a JSON-special character
// straight into the byte stream, so a caller that treats the input as JSON
// ends up with invalid JSON on the wire. RestoreUserFacing is for plain-text
// callers only — see RestoreUserFacingJSON for the JSON-safe counterpart
// used on every provider response.
func TestRegistry_RestoreUserFacing_QuoteBreaksRawJSON(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", `Robert "Bob" Johnson`)
	in := `{"name": "` + ph + `"}`
	out := reg.RestoreUserFacing(in)
	if json.Valid([]byte(out)) {
		t.Fatalf("expected verbatim restore to produce invalid JSON, got valid: %q", out)
	}
}

// TestRegistry_RestoreUserFacingJSON_EscapesQuotes is the fix: restoring
// the same PERSON through the JSON-safe path keeps the response valid JSON
// and still yields the original name once unmarshaled.
func TestRegistry_RestoreUserFacingJSON_EscapesQuotes(t *testing.T) {
	reg := NewRegistry()
	original := `Robert "Bob" Johnson`
	ph := reg.Placeholder("PERSON", original)
	in := `{"name": "` + ph + `"}`
	out := reg.RestoreUserFacingJSON(in)
	if !json.Valid([]byte(out)) {
		t.Fatalf("RestoreUserFacingJSON produced invalid JSON: %q", out)
	}
	var parsed struct{ Name string }
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body %q)", err, out)
	}
	if parsed.Name != original {
		t.Fatalf("Name = %q, want %q", parsed.Name, original)
	}
}

// TestRegistry_RestoreUserFacingJSON_EscapesNewlineAndBackslash covers the
// exact production failure mode: "invalid character '\n' in string
// literal", plus a literal backslash which would otherwise start a bogus
// escape sequence.
func TestRegistry_RestoreUserFacingJSON_EscapesNewlineAndBackslash(t *testing.T) {
	reg := NewRegistry()
	original := "123 Main St\\nApt 4\nSpringfield"
	ph := reg.Placeholder("LOCATION", original)
	in := `{"address": "` + ph + `"}`
	out := reg.RestoreUserFacingJSON(in)
	if !json.Valid([]byte(out)) {
		t.Fatalf("RestoreUserFacingJSON produced invalid JSON: %q", out)
	}
	var parsed struct{ Address string }
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body %q)", err, out)
	}
	if parsed.Address != original {
		t.Fatalf("Address = %q, want %q", parsed.Address, original)
	}
}

// TestRegistry_RestoreUserFacingJSON_DoesNotEscapeAngleBrackets checks that
// the JSON-safe restore does not add Go's default HTML-safe escaping on top
// of the required JSON escaping — angle brackets and ampersands are legal
// unescaped inside a JSON string and should come back byte-for-byte.
func TestRegistry_RestoreUserFacingJSON_DoesNotEscapeAngleBrackets(t *testing.T) {
	reg := NewRegistry()
	original := "Bourne & Hollingsworth <legal name>"
	ph := reg.Placeholder("PERSON", original)
	in := `{"name": "` + ph + `"}`
	out := reg.RestoreUserFacingJSON(in)
	if !strings.Contains(out, original) {
		t.Fatalf("expected verbatim angle brackets/ampersand in %q", out)
	}
	var parsed struct{ Name string }
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body %q)", err, out)
	}
	if parsed.Name != original {
		t.Fatalf("Name = %q, want %q", parsed.Name, original)
	}
}

// TestRegistry_RestoreStreamChunkJSON_QuoteSplitAcrossChunks exercises the
// streaming path end to end: the placeholder itself is split across chunk
// boundaries (already covered elsewhere), and once restored the
// quote-bearing original must not break the JSON accumulated so far.
func TestRegistry_RestoreStreamChunkJSON_QuoteSplitAcrossChunks(t *testing.T) {
	reg := NewRegistry()
	original := `Robert "Bob" Johnson`
	ph := reg.Placeholder("PERSON", original)
	parts := []string{`{"name": "` + ph[:4], ph[4:] + `"}`}

	var carry []byte
	var got strings.Builder
	for _, part := range parts {
		emit, newCarry := reg.RestoreStreamChunkJSON([]byte(part), carry)
		carry = newCarry
		got.Write(emit)
	}
	if tail := reg.FlushCarryJSON(carry); len(tail) > 0 {
		got.Write(tail)
	}

	out := got.String()
	if !json.Valid([]byte(out)) {
		t.Fatalf("streamed JSON restore produced invalid JSON: %q", out)
	}
	var parsed struct{ Name string }
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body %q)", err, out)
	}
	if parsed.Name != original {
		t.Fatalf("Name = %q, want %q", parsed.Name, original)
	}
}

func TestScrub_PolicyAwarePlaceholders(t *testing.T) {
	spans := []Span{
		{Start: 0, End: 8, EntityType: "PERSON", Score: 0.9},
		{Start: 9, End: 20, EntityType: "US_SSN", Score: 0.9},
	}
	reg := NewRegistry()
	res := spliceSpans("Jane Doe 222-33-4444", spans, 0.5, reg, false, true)
	if !strings.Contains(res.Text, "<PII_PERSON_1>") {
		t.Fatalf("expected PERSON placeholder in %q", res.Text)
	}
	if !strings.Contains(res.Text, "<PII_US_SSN_1>") {
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
