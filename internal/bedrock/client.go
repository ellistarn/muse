package bedrock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ellistarn/muse/internal/awsconfig"
	"github.com/ellistarn/muse/internal/llm"
	"github.com/ellistarn/muse/internal/log"
)

const (
	ModelOpus   = "claude-opus"
	ModelSonnet = "claude-sonnet"

	// defaultMaxTokens matches the AI SDK's default for Claude on Bedrock.
	// When extended thinking is enabled, the thinking budget is added on top.
	defaultMaxTokens = 4096
)

// Usage is an alias for llm.Usage so callers don't need to import both packages.
type Usage = llm.Usage

type modelPricing struct {
	inputPerToken  float64
	outputPerToken float64
}

// Bedrock on-demand pricing per token, keyed by model family substring.
// https://aws.amazon.com/bedrock/pricing/
var pricingTable = map[string]modelPricing{
	"claude-sonnet-4": {3.0 / 1_000_000, 15.0 / 1_000_000},
	"claude-opus-4":   {5.0 / 1_000_000, 25.0 / 1_000_000},
}

// lookupPricing finds pricing by matching a model family key against the full
// Bedrock model ID. Returns zero pricing if no match is found.
func lookupPricing(model string) modelPricing {
	for key, p := range pricingTable {
		if strings.Contains(model, key) {
			return p
		}
	}
	return modelPricing{}
}

// Runtime is the subset of the Bedrock SDK used by Client.
// This is the mock boundary for tests.
type Runtime interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// ToolHandler resolves a tool call by name and input, returning the result text.
type ToolHandler func(name string, input map[string]any) (string, error)

// Client wraps Bedrock's Converse API with rate limiting and retry.
type Client struct {
	runtime  Runtime
	model    string
	pricing  modelPricing
	throttle chan struct{} // token bucket: one token per request slot
}

const (
	maxRetries     = 5
	baseBackoff    = 2 * time.Second
	maxBackoff     = 60 * time.Second
	requestsPerSec = 4 // target steady-state request rate
)

func NewClient(ctx context.Context, model string) (*Client, error) {
	cfg, err := awsconfig.Load(ctx)
	if err != nil {
		return nil, err
	}
	if override := os.Getenv("MUSE_MODEL"); override != "" {
		model = override
	} else {
		resolved, err := resolveModel(ctx, cfg, model)
		if err != nil {
			return nil, err
		}
		model = resolved
	}
	c := &Client{
		runtime:  bedrockruntime.NewFromConfig(cfg),
		model:    model,
		pricing:  lookupPricing(model),
		throttle: make(chan struct{}, requestsPerSec),
	}
	// Start the token refiller: adds one request token per 1/requestsPerSec interval.
	go c.refillTokens(ctx)
	return c, nil
}

// resolveModel finds the latest US cross-region inference profile matching the
// given model family (e.g. "claude-opus" or "claude-sonnet").
func resolveModel(ctx context.Context, cfg aws.Config, family string) (string, error) {
	out, err := bedrock.NewFromConfig(cfg).ListInferenceProfiles(ctx, &bedrock.ListInferenceProfilesInput{})
	if err != nil {
		return "", fmt.Errorf("failed to list inference profiles: %w", err)
	}
	var candidates []string
	for _, p := range out.InferenceProfileSummaries {
		id := aws.ToString(p.InferenceProfileId)
		if strings.HasPrefix(id, "us.anthropic.") && strings.Contains(id, family) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no US inference profile found for %q", family)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(candidates)))
	log.Printf("discovered model %s -> %s\n", family, candidates[0])
	return candidates[0], nil
}

// NewClientWithRuntime creates a Client with a caller-provided Runtime.
// Used in tests to inject a mock Bedrock backend. The token bucket is
// pre-filled so tests don't block, and no background goroutine is started.
func NewClientWithRuntime(_ context.Context, runtime Runtime) *Client {
	// Large buffer so tests never block on rate limiting.
	throttle := make(chan struct{}, 100)
	for range 100 {
		throttle <- struct{}{}
	}
	return &Client{
		runtime:  runtime,
		model:    "test-model",
		throttle: throttle,
	}
}

// Model returns the resolved model ID (e.g. "us.anthropic.claude-opus-4-6-v1").
func (c *Client) Model() string {
	return c.model
}

// refillTokens adds request tokens at a steady rate.
func (c *Client) refillTokens(ctx context.Context) {
	ticker := time.NewTicker(time.Second / time.Duration(requestsPerSec))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case c.throttle <- struct{}{}:
			default: // bucket full, discard
			}
		}
	}
}

// Converse sends a message with a system prompt and returns the text response.
// Requests are paced by a token bucket and retried with exponential backoff on throttling errors.
func (c *Client) Converse(ctx context.Context, system, user string, opts ...llm.ConverseOption) (string, Usage, error) {
	o := llm.Apply(opts)
	messages := []types.Message{
		{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: user}},
		},
	}
	text, usage, _, _, err := c.converseRaw(ctx, system, messages, nil, o)
	return text, usage, err
}

const maxToolRounds = 3

// ConverseWithTools sends a message and handles tool use in a loop.
// The LLM can request tools up to maxToolRounds times. Once the tool loop
// completes, a final call with synthesisPrompt (without tools) produces the
// user-facing answer. This keeps intermediate reasoning and tool calls internal.
func (c *Client) ConverseWithTools(ctx context.Context, system, user string, toolConfig *types.ToolConfiguration, handler ToolHandler, synthesisPrompt string) (string, Usage, error) {
	messages := []types.Message{
		{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: user}},
		},
	}

	var totalUsage Usage
	for range maxToolRounds {
		_, usage, stop, assistantContent, err := c.converseRaw(ctx, system, messages, toolConfig, llm.ConverseOptions{})
		totalUsage = totalUsage.Add(usage)
		if err != nil {
			return "", totalUsage, err
		}
		// Append the assistant's response (text or tool calls) to the conversation
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: assistantContent,
		})
		if stop != types.StopReasonToolUse {
			break
		}
		toolResults := resolveToolCalls(assistantContent, handler)
		messages = append(messages, types.Message{
			Role:    types.ConversationRoleUser,
			Content: toolResults,
		})
	}
	// Synthesize: one final call to produce the user-facing answer. We pass
	// toolConfig so Bedrock accepts the toolUse/toolResult blocks in history.
	messages = append(messages, types.Message{
		Role:    types.ConversationRoleUser,
		Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: synthesisPrompt}},
	})
	text, usage, _, _, err := c.converseRaw(ctx, system, messages, toolConfig, llm.ConverseOptions{})
	return text, totalUsage.Add(usage), err
}

func resolveToolCalls(content []types.ContentBlock, handler ToolHandler) []types.ContentBlock {
	var results []types.ContentBlock
	for _, block := range content {
		tu, ok := block.(*types.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		var input map[string]any
		if tu.Value.Input != nil {
			if err := tu.Value.Input.UnmarshalSmithyDocument(&input); err != nil {
				input = map[string]any{}
			}
		}
		result, err := handler(aws.ToString(tu.Value.Name), input)
		if err != nil {
			results = append(results, &types.ContentBlockMemberToolResult{
				Value: types.ToolResultBlock{
					ToolUseId: tu.Value.ToolUseId,
					Status:    types.ToolResultStatusError,
					Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: err.Error()}},
				},
			})
			continue
		}
		results = append(results, &types.ContentBlockMemberToolResult{
			Value: types.ToolResultBlock{
				ToolUseId: tu.Value.ToolUseId,
				Status:    types.ToolResultStatusSuccess,
				Content:   []types.ToolResultContentBlock{&types.ToolResultContentBlockMemberText{Value: result}},
			},
		})
	}
	return results
}

func (c *Client) converseRaw(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts llm.ConverseOptions) (string, Usage, types.StopReason, []types.ContentBlock, error) {
	var lastErr error
	for attempt := range maxRetries {
		// Wait for a request token (rate limiting)
		select {
		case <-ctx.Done():
			return "", Usage{}, "", nil, ctx.Err()
		case <-c.throttle:
		}

		text, usage, stop, content, err := c.converseRawOnce(ctx, system, messages, toolConfig, opts)
		if err == nil {
			return text, usage, stop, content, nil
		}
		if !isThrottling(err) {
			return text, usage, stop, content, err
		}
		lastErr = err
		backoff := backoffDuration(attempt)
		log.Printf("  throttled (attempt %d/%d), backing off %s\n", attempt+1, maxRetries, backoff.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", Usage{}, "", nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return "", Usage{}, "", nil, fmt.Errorf("throttled after %d retries: %w", maxRetries, lastErr)
}

func (c *Client) converseRawOnce(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts llm.ConverseOptions) (string, Usage, types.StopReason, []types.ContentBlock, error) {
	maxTokens := int32(defaultMaxTokens)
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if opts.ThinkingBudget > 0 {
		maxTokens += opts.ThinkingBudget
	}
	input := &bedrockruntime.ConverseInput{
		ModelId: &c.model,
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: system},
		},
		Messages: messages,
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aws.Int32(maxTokens),
		},
	}
	if opts.ThinkingBudget > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": opts.ThinkingBudget,
			},
		})
	}
	if toolConfig != nil {
		input.ToolConfig = toolConfig
	}

	out, err := c.runtime.Converse(ctx, input)
	if err != nil {
		return "", Usage{}, "", nil, fmt.Errorf("converse failed: %w", err)
	}
	usage := c.extractUsage(out)
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", usage, out.StopReason, nil, nil
	}
	text := ""
	for _, block := range msg.Value.Content {
		if tb, ok := block.(*types.ContentBlockMemberText); ok {
			text = tb.Value
			break
		}
	}
	if out.StopReason == types.StopReasonMaxTokens {
		return text, usage, out.StopReason, msg.Value.Content, fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}
	return text, usage, out.StopReason, msg.Value.Content, nil
}

func (c *Client) extractUsage(out *bedrockruntime.ConverseOutput) Usage {
	var usage Usage
	if out.Usage != nil {
		if out.Usage.InputTokens != nil {
			usage.InputTokens = int(*out.Usage.InputTokens)
		}
		if out.Usage.OutputTokens != nil {
			usage.OutputTokens = int(*out.Usage.OutputTokens)
		}
	}
	usage.Cost_ = float64(usage.InputTokens)*c.pricing.inputPerToken + float64(usage.OutputTokens)*c.pricing.outputPerToken
	return usage
}

// isThrottling checks whether the error is a Bedrock throttling (429) response.
func isThrottling(err error) bool {
	// Check for smithy HTTP response with 429 status
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 429 {
		return true
	}
	// Fallback: check error string for ThrottlingException
	return strings.Contains(err.Error(), "ThrottlingException") || strings.Contains(err.Error(), "Too many tokens")
}

// backoffDuration returns jittered exponential backoff for the given attempt.
func backoffDuration(attempt int) time.Duration {
	backoff := float64(baseBackoff) * math.Pow(2, float64(attempt))
	if backoff > float64(maxBackoff) {
		backoff = float64(maxBackoff)
	}
	// Add jitter: 50-100% of calculated backoff
	jitter := 0.5 + rand.Float64()*0.5
	return time.Duration(backoff * jitter)
}
