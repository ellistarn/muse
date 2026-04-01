package inference

import "context"

// Message is a provider-agnostic conversation message.
type Message struct {
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
}

// Response holds the output from a conversation call.
type Response struct {
	Text  string
	Usage Usage
}

// Client is the inference interface. Providers implement multi-turn
// conversation with optional streaming. Single-turn callers use the
// Converse/ConverseStream free functions.
type Client interface {
	ConverseMessages(ctx context.Context, system string, messages []Message, opts ...ConverseOption) (*Response, error)
	ConverseMessagesStream(ctx context.Context, system string, messages []Message, fn StreamFunc, opts ...ConverseOption) (*Response, error)
	Model() string
}

// Converse is a convenience for single-turn calls.
func Converse(ctx context.Context, c Client, system, user string, opts ...ConverseOption) (string, Usage, error) {
	resp, err := c.ConverseMessages(ctx, system, []Message{{Role: "user", Content: user}}, opts...)
	if resp == nil {
		return "", Usage{}, err
	}
	return resp.Text, resp.Usage, err
}

// ConverseStream is a convenience for single-turn streaming calls.
func ConverseStream(ctx context.Context, c Client, system, user string, fn StreamFunc, opts ...ConverseOption) (string, Usage, error) {
	resp, err := c.ConverseMessagesStream(ctx, system, []Message{{Role: "user", Content: user}}, fn, opts...)
	if resp == nil {
		return "", Usage{}, err
	}
	return resp.Text, resp.Usage, err
}
