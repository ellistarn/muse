package inference

// EstimateTokens returns an approximate token count for a string.
// Claude's tokenizer averages ~4.5 characters per token on English prose,
// measured empirically against Bedrock's reported usage on representative
// documents (soul.md, prompt templates). Use provider-reported usage for
// operational costs; use this for sizing artifacts.
func EstimateTokens(s string) int {
	return len(s) * 2 / 9 // ~4.5 chars per token
}
