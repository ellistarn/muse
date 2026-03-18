package inference

// ConverseOption configures a Converse call.
type ConverseOption func(*ConverseOptions)

// ConverseOptions holds per-call overrides.
type ConverseOptions struct {
	MaxTokens      int32 // 0 means use the client default
	ThinkingBudget int32 // 0 means no extended thinking
}

// Apply returns the merged options.
func Apply(opts []ConverseOption) ConverseOptions {
	var o ConverseOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// WithMaxTokens caps the output token count for a single call.
func WithMaxTokens(n int32) ConverseOption {
	return func(o *ConverseOptions) { o.MaxTokens = n }
}

// WithThinking enables extended thinking with the given budget.
// MaxTokens is automatically increased to accommodate the thinking budget
// on top of the text output allocation, matching AI SDK behavior.
func WithThinking(budgetTokens int32) ConverseOption {
	return func(o *ConverseOptions) { o.ThinkingBudget = budgetTokens }
}

// StreamDelta is a chunk of streamed output from the model.
type StreamDelta struct {
	Text     string
	Thinking bool // true for reasoning tokens, false for response tokens
}

// StreamFunc receives streaming deltas as they arrive from the model.
type StreamFunc func(StreamDelta)

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
