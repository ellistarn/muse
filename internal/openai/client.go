package openai

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/throttle"
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

// OpenAI rate limits vary by tier. Tier 5: 10,000 RPM ≈ 166 req/s.
// Seed conservatively — AIMD will find the real ceiling.
const (
	openaiSeedRate = 50.0
	openaiMaxRate  = 166.0
)

// Client wraps the OpenAI Chat Completions API with adaptive rate limiting.
// Rate limiting: applied via throttle.Retry around each API call, same pattern
// as Anthropic — the SDK lacks typed status code errors.
type Client struct {
	client  openai.Client
	model   string
	pricing modelPricing
	limiter throttle.Limiter
}

const (
	ModelFull      = "gpt-4.1"
	ModelMini      = "gpt-4.1-mini"
	ModelReasoning = "o3"
)

// NewClient creates an OpenAI API client with adaptive rate limiting.
// Reads OPENAI_API_KEY from env by default. model should be a concrete
// OpenAI model ID like "gpt-4.1".
func NewClient(ctx context.Context, model string, opts ...option.RequestOption) (*Client, error) {
	sdk := openai.NewClient(opts...)
	return &Client{
		client:  sdk,
		model:   model,
		pricing: lookupPricing(model),
		limiter: throttle.NewAIMDLimiter(ctx, throttle.Config{
			SeedRate: openaiSeedRate,
			MaxRate:  openaiMaxRate,
			Label:    "openai",
		}),
	}, nil
}

func (c *Client) Model() string {
	return c.model
}

func (c *Client) ConverseMessages(ctx context.Context, system string, messages []inference.Message, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)

	var result *inference.Response
	err := throttle.Retry(ctx, c.limiter, throttle.DefaultRetryConfig(), isThrottled, func() error {
		completion, err := c.client.Chat.Completions.New(ctx, params)
		if err != nil {
			if cse, ok := classifyContextSizeError(err); ok {
				return cse
			}
			return fmt.Errorf("openai chat.completions.new: %w", err)
		}

		text := ""
		finishReason := ""
		if len(completion.Choices) > 0 {
			text = completion.Choices[0].Message.Content
			finishReason = completion.Choices[0].FinishReason
		}
		usage := c.extractUsage(completion.Usage)
		result = &inference.Response{Text: text, Usage: usage}

		if finishReason == "length" {
			return &inference.TruncatedError{OutputTokens: usage.OutputTokens}
		}
		return nil
	})
	return result, err
}

// NOTE: Retry wraps the entire stream. If a throttle error occurs mid-stream
// after fn has already received partial deltas, the retry will re-deliver from
// the beginning. This is acceptable for interactive/terminal use (the compose
// pipeline uses ConverseMessages, not streaming). Do not use this in batch
// pipelines without buffering or idempotent delivery.
func (c *Client) ConverseMessagesStream(ctx context.Context, system string, messages []inference.Message, fn inference.StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	params := c.buildParams(system, messages, o)
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	var result *inference.Response
	err := throttle.Retry(ctx, c.limiter, throttle.DefaultRetryConfig(), isThrottled, func() error {
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
			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
				usage = c.extractUsage(chunk.Usage)
			}
		}

		if err := stream.Err(); err != nil {
			if cse, ok := classifyContextSizeError(err); ok {
				return cse
			}
			return fmt.Errorf("openai stream: %w", err)
		}

		fullText := text.String()
		result = &inference.Response{Text: fullText, Usage: usage}

		if finishReason == "length" {
			return &inference.TruncatedError{OutputTokens: usage.OutputTokens}
		}
		return nil
	})
	return result, err
}

// isThrottled checks if an OpenAI SDK error is a rate limit (HTTP 429).
func isThrottled(err error) bool {
	return strings.Contains(err.Error(), fmt.Sprintf("%d", http.StatusTooManyRequests)) ||
		strings.Contains(err.Error(), "rate_limit")
}

// classifyContextSizeError detects errors indicating the prompt did not fit
// the model's context window and returns a typed inference.ContextSizeError.
// Recognized patterns:
//   - llama.cpp: "exceeds the available context size" with type "exceed_context_size_error"
//     and JSON fields n_prompt_tokens / n_ctx
//   - OpenAI cloud: "context_length_exceeded" or "maximum context length"
//
// Returns (nil, false) if the error doesn't match any known context-size pattern.
func classifyContextSizeError(err error) (*inference.ContextSizeError, bool) {
	msg := err.Error()
	if !strings.Contains(msg, "exceed_context_size") &&
		!strings.Contains(msg, "exceeds the available context size") &&
		!strings.Contains(msg, "context_length_exceeded") &&
		!strings.Contains(msg, "maximum context length") {
		return nil, false
	}
	cse := &inference.ContextSizeError{}
	if m := contextSizePromptRe.FindStringSubmatch(msg); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			cse.PromptTokens = n
		}
	}
	if m := contextSizeCtxRe.FindStringSubmatch(msg); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			cse.ContextSize = n
		}
	}
	return cse, true
}

var (
	contextSizePromptRe = regexp.MustCompile(`"n_prompt_tokens"\s*:\s*(\d+)`)
	contextSizeCtxRe    = regexp.MustCompile(`"n_ctx"\s*:\s*(\d+)`)
)

func (c *Client) buildParams(system string, messages []inference.Message, opts inference.ConverseOptions) openai.ChatCompletionNewParams {
	maxTokens := int64(inference.DefaultMaxTokens)
	if opts.MaxTokens > 0 {
		maxTokens = int64(opts.MaxTokens)
	}
	if opts.ThinkingBudget > 0 && isReasoningModel(c.model) {
		maxTokens += int64(opts.ThinkingBudget)
	}

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
