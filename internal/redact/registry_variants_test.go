package redact

import (
	"strings"
	"testing"
)

func TestRegistry_MaskRestoresLLMPlaceholderVariants(t *testing.T) {
	reg := NewRegistry()
	ph := reg.Placeholder("PERSON", "Eric Hagman")
	if ph != "<PII_PERSON_1>" {
		t.Fatalf("placeholder = %q", ph)
	}
	inner := "PII_PERSON_1"

	cases := []struct {
		name string
		in   string
	}{
		{"wire-angle", ph},
		{"square-brackets", "[PII_PERSON_1]"},
		{"markdown-bold-square", "**[PII_PERSON_1]**"},
		{"curly", "{PII_PERSON_1}"},
		{"parens", "(PII_PERSON_1)"},
		{"spaced-square", "[ PII_PERSON_1 ]"},
		{"spaced-angle", "< PII_PERSON_1 >"},
		{"json-escaped-square", `\u005b` + inner + `\u005d`},
		{"html-square-entities", "&#91;" + inner + "&#93;"},
		{"json-escaped-angle", jsonEscapedPlaceholder(ph)},
		{"html-escaped-angle", htmlEscapedPlaceholder(ph)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := reg.RestoreUserFacing(tc.in)
			if !strings.Contains(out, "Eric Hagman") {
				t.Fatalf("restore = %q", out)
			}
			if reg.MaskPlaceholdersRemaining(out) != 0 {
				t.Fatalf("leaked placeholder in %q", out)
			}
		})
	}
}

func TestRegistry_MaskRestoresNumberedTokenWithoutPrefixCollision(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	out := reg.RestoreUserFacing("also registered [PII_PERSON_10] here")
	if strings.Contains(out, "Eric Hagman0") || strings.Contains(out, "Eric Hagman]") {
		t.Fatalf("prefix collision corrupted output: %q", out)
	}
	if !strings.Contains(out, "[PII_PERSON_10]") {
		t.Fatalf("unregistered token should stay opaque: %q", out)
	}
}

func TestRegistry_BareInnerTokenNotRestored(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	out := reg.RestoreUserFacing("name is PII_PERSON_1 today")
	if strings.Contains(out, "Eric Hagman") {
		t.Fatalf("bare inner token must not restore: %q", out)
	}
	if reg.MaskPlaceholdersRemaining(out) != 0 {
		t.Fatalf("bare token should not count as MASK leak: %q", out)
	}
}

func TestRegistry_MaskPlaceholdersRemaining_DetectsSquareBracketLeak(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	if got := reg.MaskPlaceholdersRemaining("Your name is [PII_PERSON_1]"); got != 1 {
		t.Fatalf("remaining = %d, want 1", got)
	}
}

func TestRegistry_MaskPlaceholdersRemaining_IgnoresSealTokens(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("US_SSN", "222-33-4444")
	if got := reg.MaskPlaceholdersRemaining("ssn [PII_US_SSN_1]"); got != 0 {
		t.Fatalf("SEAL placeholder should not count as MASK leak: remaining=%d", got)
	}
}

func TestReformatWirePlaceholderDelimiters(t *testing.T) {
	in := "email <PII_EMAIL_ADDRESS_1> and name <PII_PERSON_2>"
	got := ReformatWirePlaceholderDelimiters(in, '[', ']')
	want := "email [PII_EMAIL_ADDRESS_1] and name [PII_PERSON_2]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestReformatSpacedWirePlaceholderDelimiters(t *testing.T) {
	in := "<PII_PERSON_1>"
	got := ReformatSpacedWirePlaceholderDelimiters(in, '[', ']')
	want := "[ PII_PERSON_1 ]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestStreamSafePrefixLen_IgnoresUnrelatedOpenBracket(t *testing.T) {
	combined := []byte("hello (world")
	if got := streamSafePrefixLen(combined); got != len(combined) {
		t.Fatalf("safeLen = %d, want full buffer %d", got, len(combined))
	}
}

func TestStreamSafePrefixLen_HoldsIncompletePIIPlaceholder(t *testing.T) {
	combined := []byte(`prefix [PII_PERSON_`)
	if got := streamSafePrefixLen(combined); got != 7 {
		t.Fatalf("safeLen = %d, want 7", got)
	}
}

func TestRegistry_RestoreStreamChunk_SplitSquareBracketPlaceholder(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	runStreamSplitRestoreTest(t, reg, "[PII_PERSON_1]")
}

func TestRegistry_RestoreStreamChunk_SplitCurlyPlaceholder(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	runStreamSplitRestoreTest(t, reg, "{PII_PERSON_1}")
}

func TestRegistry_RestoreStreamChunk_SplitParenPlaceholder(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	runStreamSplitRestoreTest(t, reg, "(PII_PERSON_1)")
}

func TestRegistry_RestoreStreamChunk_SplitSpacedSquarePlaceholder(t *testing.T) {
	reg := NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	runStreamSplitRestoreTest(t, reg, "[ PII_PERSON_1 ]")
}

func runStreamSplitRestoreTest(t *testing.T, reg *Registry, token string) {
	t.Helper()
	mid := len(token) / 2
	if mid == 0 {
		mid = 1
	}
	parts := []string{"prefix ", token[:mid], token[mid:] + " suffix"}
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
	want := "prefix Eric Hagman suffix"
	if got.String() != want {
		t.Fatalf("stream restore = %q want %q", got.String(), want)
	}
}
