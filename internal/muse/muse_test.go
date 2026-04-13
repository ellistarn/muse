package muse

import (
	"context"
	"testing"

	"github.com/ellistarn/muse/internal/inference"
)

// capturingClient records the ConverseOptions passed to each call so tests
// can assert that callers (like Ask) set the right options.
type capturingClient struct {
	opts []inference.ConverseOptions // captured from the last call
}

func (c *capturingClient) ConverseMessages(_ context.Context, _ string, _ []inference.Message, opts ...inference.ConverseOption) (*inference.Response, error) {
	c.opts = append(c.opts, inference.Apply(opts))
	return &inference.Response{Text: "ok"}, nil
}

func (c *capturingClient) ConverseMessagesStream(_ context.Context, _ string, _ []inference.Message, fn inference.StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	c.opts = append(c.opts, inference.Apply(opts))
	if fn != nil {
		fn(inference.StreamDelta{Text: "ok"})
	}
	return &inference.Response{Text: "ok"}, nil
}

func (c *capturingClient) Model() string { return "test" }

func TestAsk_EnablesThinking(t *testing.T) {
	client := &capturingClient{}
	m := New(client, "test document")

	_, err := m.Ask(context.Background(), AskInput{
		Question: "hello",
	})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}

	if len(client.opts) != 1 {
		t.Fatalf("expected 1 call, got %d", len(client.opts))
	}
	if client.opts[0].ThinkingBudget != inference.DefaultThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d", client.opts[0].ThinkingBudget, inference.DefaultThinkingBudget)
	}
}

func TestAsk_EnablesThinkingWithStreaming(t *testing.T) {
	client := &capturingClient{}
	m := New(client, "test document")

	_, err := m.Ask(context.Background(), AskInput{
		Question:   "hello",
		StreamFunc: func(delta inference.StreamDelta) {},
	})
	if err != nil {
		t.Fatalf("Ask failed: %v", err)
	}

	if len(client.opts) != 1 {
		t.Fatalf("expected 1 call, got %d", len(client.opts))
	}
	if client.opts[0].ThinkingBudget != inference.DefaultThinkingBudget {
		t.Errorf("ThinkingBudget = %d, want %d", client.opts[0].ThinkingBudget, inference.DefaultThinkingBudget)
	}
}
