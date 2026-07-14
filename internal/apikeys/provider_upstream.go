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
// always receive a placeholder — outbound auth is SigV4, so never persist a
// caller-supplied actual_key for them.
func ResolveActualKey(provider, actualKey string) string {
	if ProviderUsesAWSAuth(provider) {
		return AWSAuthProviderPlaceholderKey
	}
	return actualKey
}
