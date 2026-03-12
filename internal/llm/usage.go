package llm

// Usage tracks token consumption and cost from an LLM call.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	InputPricePerToken  float64
	OutputPricePerToken float64
}

// Cost returns the estimated dollar cost for this usage.
func (u Usage) Cost() float64 {
	return float64(u.InputTokens)*u.InputPricePerToken + float64(u.OutputTokens)*u.OutputPricePerToken
}

// Add combines two Usage values, preserving pricing from whichever has it.
func (u Usage) Add(other Usage) Usage {
	result := Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
	}
	if u.InputPricePerToken != 0 {
		result.InputPricePerToken = u.InputPricePerToken
		result.OutputPricePerToken = u.OutputPricePerToken
	} else {
		result.InputPricePerToken = other.InputPricePerToken
		result.OutputPricePerToken = other.OutputPricePerToken
	}
	return result
}
