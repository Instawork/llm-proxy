package apikeys

// AWSAuthProviderPlaceholderKey is stored for providers that authenticate
// upstream via AWS SigV4 instead of a provider bearer token.
const AWSAuthProviderPlaceholderKey = "unused"

const bedrockMantleProvider = "bedrock-mantle"

// IsBedrockFamilyProvider reports proxy-key providers that authenticate Bedrock
// traffic (Converse identity keys and Mantle caller keys share the bedrock
// provider; legacy bedrock-mantle keys remain valid).
func IsBedrockFamilyProvider(provider string) bool {
	switch normalizeProvider(provider) {
	case BedrockProvider, bedrockMantleProvider:
		return true
	default:
		return false
	}
}

// ProviderUsesAWSAuth reports providers whose outbound calls use AWS SigV4
// rather than the stored actual_key.
func ProviderUsesAWSAuth(provider string) bool {
	return IsBedrockFamilyProvider(provider)
}

// ResolveActualKey returns the upstream credential to store. AWS-auth providers
// accept an empty request actual_key and receive a placeholder.
func ResolveActualKey(provider, actualKey string) string {
	if actualKey != "" {
		return actualKey
	}
	if ProviderUsesAWSAuth(provider) {
		return AWSAuthProviderPlaceholderKey
	}
	return ""
}
