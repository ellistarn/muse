package openai

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ellistarn/muse/internal/inference"
)

type modelPricing = inference.Pricing

// OpenAI pricing per token.
// https://openai.com/api/pricing/
var pricingTable = map[string]modelPricing{
	"gpt-4.1":      {InputPerToken: 2.0 / 1_000_000, OutputPerToken: 8.0 / 1_000_000},
	"gpt-4.1-mini": {InputPerToken: 0.4 / 1_000_000, OutputPerToken: 1.6 / 1_000_000},
	"gpt-4.1-nano": {InputPerToken: 0.1 / 1_000_000, OutputPerToken: 0.4 / 1_000_000},
	"gpt-4o":       {InputPerToken: 2.5 / 1_000_000, OutputPerToken: 10.0 / 1_000_000},
	"gpt-4o-mini":  {InputPerToken: 0.15 / 1_000_000, OutputPerToken: 0.6 / 1_000_000},
	"o3":           {InputPerToken: 2.0 / 1_000_000, OutputPerToken: 8.0 / 1_000_000},
	"o4-mini":      {InputPerToken: 1.1 / 1_000_000, OutputPerToken: 4.4 / 1_000_000},
}

func lookupPricing(model string) modelPricing {
	bestKey := ""
	bestPricing := modelPricing{}
	for key, p := range pricingTable {
		if strings.Contains(model, key) && len(key) > len(bestKey) {
			bestKey = key
			bestPricing = p
		}
	}
	return bestPricing
}

func isReasoningModel(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4")
}

func reasoningEffortForBudget(budget int32) openai.ReasoningEffort {
	switch {
	case budget >= 12000:
		return openai.ReasoningEffortHigh
	case budget >= 4000:
		return openai.ReasoningEffortMedium
	default:
		return openai.ReasoningEffortLow
	}
}

// Client wraps the OpenAI Chat Completions API.
type Client struct {
	client  openai.Client
	model   string
	pricing modelPricing
}

const (
	ModelFull      = "gpt-4.1"
	ModelMini      = "gpt-4.1-mini"
	ModelReasoning = "o3"
)

// NewClient creates an OpenAI API client. Reads OPENAI_API_KEY from env
// by default. model should be a concrete OpenAI model ID like "gpt-4.1".
func NewClient(model string, opts ...option.RequestOption) (*Client, error) {
	sdk := openai.NewClient(opts...)
	return &Client{
		client:  sdk,
		model:   model,
		pricing: lookupPricing(model),
	}, nil
}

func (c *Client) Model() string {
	return c.model
}

func (c *Client) ConverseMessages(ctx context.Context, system string, messages []inference.Message, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)

	completion, err := c.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat.completions.new: %w", err)
	}

	text := ""
	finishReason := ""
	if len(completion.Choices) > 0 {
		text = completion.Choices[0].Message.Content
		finishReason = completion.Choices[0].FinishReason
	}
	usage := c.extractUsage(completion.Usage)

	if finishReason == "length" {
		return &inference.Response{Text: text, Usage: usage},
			fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}

	return &inference.Response{Text: text, Usage: usage}, nil
}

func (c *Client) ConverseMessagesStream(ctx context.Context, system string, messages []inference.Message, fn inference.StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	stream := c.client.Chat.Completions.NewStreaming(ctx, params)

	var text strings.Builder
	var usage inference.Usage
	var finishReason string

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta.Content
			if delta != "" {
				text.WriteString(delta)
				if fn != nil {
					fn(inference.StreamDelta{Text: delta})
				}
			}
			if chunk.Choices[0].FinishReason != "" {
				finishReason = chunk.Choices[0].FinishReason
			}
		}
		// Usage comes in the final chunk when stream_options.include_usage is true.
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			usage = c.extractUsage(chunk.Usage)
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("openai stream: %w", err)
	}

	fullText := text.String()

	if finishReason == "length" {
		return &inference.Response{Text: fullText, Usage: usage},
			fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}

	return &inference.Response{Text: fullText, Usage: usage}, nil
}

func (c *Client) buildParams(system string, messages []inference.Message, opts inference.ConverseOptions) openai.ChatCompletionNewParams {
	maxTokens := int64(inference.DefaultMaxTokens)
	if opts.MaxTokens > 0 {
		maxTokens = int64(opts.MaxTokens)
	}
	if opts.ThinkingBudget > 0 && isReasoningModel(c.model) {
		maxTokens += int64(opts.ThinkingBudget)
	}

	// Build message list: system message first, then conversation.
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+1)
	msgs = append(msgs, openai.SystemMessage(system))
	for _, m := range messages {
		if m.Role == "assistant" {
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		} else {
			msgs = append(msgs, openai.UserMessage(m.Content))
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:               c.model,
		Messages:            msgs,
		MaxCompletionTokens: openai.Int(maxTokens),
	}
	if opts.ThinkingBudget > 0 && isReasoningModel(c.model) {
		params.ReasoningEffort = reasoningEffortForBudget(opts.ThinkingBudget)
	}
	return params
}

func (c *Client) extractUsage(usage openai.CompletionUsage) inference.Usage {
	in := int(usage.PromptTokens)
	out := int(usage.CompletionTokens)
	return inference.NewUsage(in, out, c.pricing.ComputeCost(in, out))
}
