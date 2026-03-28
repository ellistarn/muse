package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/throttle"
)

// Model family constants matching bedrock's naming.
const (
	ModelOpus   = "opus"
	ModelSonnet = "sonnet"
)

type modelPricing = inference.Pricing

// Anthropic API pricing per token.
// https://docs.anthropic.com/en/docs/about-claude/models
var pricingTable = map[string]modelPricing{
	"sonnet": {InputPerToken: 3.0 / 1_000_000, OutputPerToken: 15.0 / 1_000_000},
	"opus":   {InputPerToken: 5.0 / 1_000_000, OutputPerToken: 25.0 / 1_000_000},
}

// Anthropic Tier 4 rate limits: 4000 RPM ≈ 66 req/s
const (
	anthropicSeedRate = 60.0
	anthropicMaxRate  = 66.0
)

// Client wraps the Anthropic Messages API with adaptive rate limiting.
type Client struct {
	client  anthropic.Client
	model   string
	family  string // "opus" or "sonnet", for pricing lookup
	pricing modelPricing
	limiter throttle.Limiter
}

// NewClient creates an Anthropic API client with adaptive rate limiting.
// Reads ANTHROPIC_API_KEY from env by default. model should be "opus" or "sonnet".
func NewClient(ctx context.Context, model string, opts ...option.RequestOption) (*Client, error) {
	sdk := anthropic.NewClient(opts...)
	resolved, family := resolveModel(model)
	p := pricingTable[family]
	return &Client{
		client:  sdk,
		model:   resolved,
		family:  family,
		pricing: p,
		limiter: throttle.NewAIMDLimiter(ctx, throttle.Config{
			SeedRate: anthropicSeedRate,
			MaxRate:  anthropicMaxRate,
			Label:    "anthropic",
		}),
	}, nil
}

func resolveModel(family string) (string, string) {
	switch family {
	case ModelOpus, "claude-opus":
		return string(anthropic.ModelClaudeOpus4_6), "opus"
	case ModelSonnet, "claude-sonnet":
		return string(anthropic.ModelClaudeSonnet4_6), "sonnet"
	default:
		// Allow passing a full model ID directly.
		return family, family
	}
}

func (c *Client) Model() string {
	return c.model
}

func (c *Client) ConverseMessages(ctx context.Context, system string, messages []inference.Message, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)

	var result *inference.Response
	err := throttle.Retry(ctx, c.limiter, throttle.DefaultRetryConfig(), isThrottled, func() error {
		msg, err := c.client.Messages.New(ctx, params)
		if err != nil {
			return fmt.Errorf("anthropic messages.new: %w", err)
		}

		text := extractText(msg)
		usage := c.extractUsage(msg)
		result = &inference.Response{Text: text, Usage: usage}

		if msg.StopReason == anthropic.StopReasonMaxTokens {
			return fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
		}
		return nil
	})
	return result, err
}

func (c *Client) ConverseMessagesStream(ctx context.Context, system string, messages []inference.Message, fn inference.StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)

	var result *inference.Response
	err := throttle.Retry(ctx, c.limiter, throttle.DefaultRetryConfig(), isThrottled, func() error {
		stream := c.client.Messages.NewStreaming(ctx, params)

		var text strings.Builder
		var usage inference.Usage
		accumulated := anthropic.Message{}

		for stream.Next() {
			event := stream.Current()
			accumulated.Accumulate(event)

			switch ev := event.AsAny().(type) {
			case anthropic.ContentBlockDeltaEvent:
				switch delta := ev.Delta.AsAny().(type) {
				case anthropic.TextDelta:
					text.WriteString(delta.Text)
					if fn != nil {
						fn(inference.StreamDelta{Text: delta.Text})
					}
				case anthropic.ThinkingDelta:
					if fn != nil {
						fn(inference.StreamDelta{Text: delta.Thinking, Thinking: true})
					}
				}
			}
		}

		if err := stream.Err(); err != nil {
			return fmt.Errorf("anthropic stream: %w", err)
		}

		usage = c.extractUsage(&accumulated)
		fullText := text.String()
		result = &inference.Response{Text: fullText, Usage: usage}

		if accumulated.StopReason == anthropic.StopReasonMaxTokens {
			return fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
		}
		return nil
	})
	return result, err
}

// isThrottled checks if an Anthropic SDK error is a rate limit (HTTP 429).
func isThrottled(err error) bool {
	// The Anthropic SDK wraps HTTP errors. Check for 429 via the response.
	type statusCoder interface {
		Error() string
	}
	// Walk the error chain looking for an HTTP response with 429
	var httpErr interface{ DumpResponse(bool) []byte }
	if errors.As(err, &httpErr) {
		// Check the error message for 429 status
		if strings.Contains(err.Error(), fmt.Sprintf("%d", http.StatusTooManyRequests)) {
			return true
		}
	}
	// Fallback: check error text
	return strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit")
}

func (c *Client) buildParams(system string, messages []inference.Message, opts inference.ConverseOptions) anthropic.MessageNewParams {
	maxTokens := int64(inference.DefaultMaxTokens)
	if opts.MaxTokens > 0 {
		maxTokens = int64(opts.MaxTokens)
	}
	if opts.ThinkingBudget > 0 {
		maxTokens += int64(opts.ThinkingBudget)
	}

	params := anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{
			{Text: system},
		},
		Messages: toAnthropicMessages(messages),
	}

	if opts.ThinkingBudget > 0 {
		params.Thinking = anthropic.ThinkingConfigParamOfEnabled(int64(opts.ThinkingBudget))
	}

	return params
}

func toAnthropicMessages(messages []inference.Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, len(messages))
	for i, m := range messages {
		if m.Role == "assistant" {
			out[i] = anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content))
		} else {
			out[i] = anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content))
		}
	}
	return out
}

func extractText(msg *anthropic.Message) string {
	var parts []string
	for _, block := range msg.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

func (c *Client) extractUsage(msg *anthropic.Message) inference.Usage {
	in := int(msg.Usage.InputTokens)
	out := int(msg.Usage.OutputTokens)
	return inference.NewUsage(in, out, c.pricing.ComputeCost(in, out))
}
