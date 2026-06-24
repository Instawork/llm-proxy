package redact

import (
	"net"
	"strings"
	"testing"
)

func TestPrepareJSONForAnalysis_SkipsToolSchemaValues(t *testing.T) {
	body := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"Hi Jess"}],"tools":[{"name":"GetBookingTemplateDetails","input_schema":{"properties":{"max":{"title":"Max Multiplier","description":"Per-position multiplier"}}}}]}`
	analyzed := prepareJSONForAnalysis(body)

	for _, hidden := range []string{
		"GetBookingTemplateDetails",
		"Max Multiplier",
		"Per-position multiplier",
		"input_schema",
		"properties",
		"title",
		"description",
	} {
		if strings.Contains(analyzed, hidden) {
			t.Fatalf("schema fragment %q should be hidden from analyzer text: %q", hidden, analyzed)
		}
	}
	if !strings.Contains(analyzed, "Hi Jess") {
		t.Fatalf("message content should remain visible to analyzer: %q", analyzed)
	}
}

func TestPrepareJSONForAnalysis_ScansToolCallArguments(t *testing.T) {
	body := `{"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"lookup","arguments":"{\"email\":\"alice@example.com\"}"}}]}]}`
	analyzed := prepareJSONForAnalysis(body)

	if !strings.Contains(analyzed, "alice@example.com") {
		t.Fatalf("tool call arguments should remain visible to analyzer: %q", analyzed)
	}
	if strings.Contains(analyzed, "lookup") {
		t.Fatalf("function name under tool_calls should be hidden: %q", analyzed)
	}
}

func TestPrepareJSONForAnalysis_PreservesOffsetsForMessageContent(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"José in Massachusetts"}]}`
	analyzed := prepareJSONForAnalysis(body)

	for _, value := range []string{"José", "Massachusetts"} {
		originalSpan := spanForValue(t, body, value, "PERSON")
		analyzedSpan := spanForValue(t, analyzed, value, "PERSON")
		if originalSpan.Start != analyzedSpan.Start || originalSpan.End != analyzedSpan.End {
			t.Fatalf("span for %q moved from (%d,%d) to (%d,%d)",
				value, originalSpan.Start, originalSpan.End, analyzedSpan.Start, analyzedSpan.End)
		}
	}
}

func TestIsPrivateOrLoopbackIP(t *testing.T) {
	classA := net.IPv4(10, 0, 0, 5).String()
	classB := net.IPv4(172, 16, 0, 1).String()
	classC := net.IPv4(192, 168, 1, 1).String()
	cases := []struct {
		value string
		want  bool
	}{
		{"127.0.0.1", true},
		{classA, true},
		{classC, true},
		{classB, true},
		{"localhost", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}
	for _, tc := range cases {
		if got := isPrivateOrLoopbackIP(tc.value); got != tc.want {
			t.Fatalf("isPrivateOrLoopbackIP(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestIsTestEmail(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"alice@example.com", true},
		{"test@company.com", true},
		{"dev@fixture.test", true},
		{"real.user@gmail.com", false},
	}
	for _, tc := range cases {
		if got := isTestEmail(tc.value); got != tc.want {
			t.Fatalf("isTestEmail(%q) = %v, want %v", tc.value, got, tc.want)
		}
	}
}
