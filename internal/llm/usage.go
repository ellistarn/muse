package llm

// Usage tracks token consumption and cost from an LLM call.
type Usage struct {
	InputTokens  int
	OutputTokens int
	Cost_        float64 // accumulated dollar cost
}

// Cost returns the estimated dollar cost for this usage.
func (u Usage) Cost() float64 {
	return u.Cost_
}

// Add combines two Usage values.
func (u Usage) Add(other Usage) Usage {
	return Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
		Cost_:        u.Cost_ + other.Cost_,
	}
}
