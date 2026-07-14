package apikeys

import "testing"

func TestProviderUsesAWSAuth(t *testing.T) {
	for _, provider := range []string{"bedrock", "bedrock-mantle", "BEDROCK-MANTLE"} {
		if !ProviderUsesAWSAuth(provider) {
			t.Fatalf("ProviderUsesAWSAuth(%q) = false, want true", provider)
		}
		if !IsBedrockFamilyProvider(provider) {
			t.Fatalf("IsBedrockFamilyProvider(%q) = false, want true", provider)
		}
	}
	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		if ProviderUsesAWSAuth(provider) {
			t.Fatalf("ProviderUsesAWSAuth(%q) = true, want false", provider)
		}
	}
}

func TestResolveActualKey(t *testing.T) {
	if got := ResolveActualKey("bedrock", ""); got != AWSAuthProviderPlaceholderKey {
		t.Fatalf("ResolveActualKey(bedrock, empty) = %q", got)
	}
	if got := ResolveActualKey("bedrock-mantle", ""); got != AWSAuthProviderPlaceholderKey {
		t.Fatalf("ResolveActualKey(bedrock-mantle, empty) = %q", got)
	}
	if got := ResolveActualKey("bedrock", "sk-caller-supplied"); got != AWSAuthProviderPlaceholderKey {
		t.Fatalf("ResolveActualKey(bedrock, caller key) = %q, want placeholder", got)
	}
	if got := ResolveActualKey("openai", ""); got != "" {
		t.Fatalf("ResolveActualKey(openai, empty) = %q, want empty", got)
	}
	if got := ResolveActualKey("openai", "sk-real"); got != "sk-real" {
		t.Fatalf("ResolveActualKey(openai, sk-real) = %q", got)
	}
}
