package inference

import (
	"context"
	"errors"
	"fmt"
)

// Message is a provider-agnostic conversation message.
type Message struct {
	Role    string // "user" or "assistant"
	Content string
}

// Response holds the output from a conversation call.
type Response struct {
	Text  string
	Usage Usage
}

// TruncatedError indicates the response was cut short by the max token limit.
// All providers return partial content alongside this error — callers can
// parse resp.Text even when the error is present.
type TruncatedError struct {
	OutputTokens int
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf("response truncated: hit max token limit (%d output tokens)", e.OutputTokens)
}

// IsTruncated reports whether err is a max-token truncation error.
func IsTruncated(err error) bool {
	var te *TruncatedError
	return errors.As(err, &te)
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
