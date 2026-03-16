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
	"github.com/ellistarn/muse/internal/inference"
)

const (
	ModelOpus   = "claude-opus"
	ModelSonnet = "claude-sonnet"

	// defaultMaxTokens matches the AI SDK's default for Claude on Bedrock.
	// When extended thinking is enabled, the thinking budget is added on top.
	defaultMaxTokens = 4096
)

// Usage is an alias for inference.Usage so callers don't need to import both packages.
type Usage = inference.Usage

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

// StreamingRuntime extends Runtime with streaming support.
// The real bedrockruntime.Client satisfies both interfaces.
type StreamingRuntime interface {
	Runtime
	ConverseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
}

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
	fmt.Fprintf(os.Stderr, "discovered model %s -> %s\n", family, candidates[0])
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
func (c *Client) Converse(ctx context.Context, system, user string, opts ...inference.ConverseOption) (string, Usage, error) {
	o := inference.Apply(opts)
	messages := []types.Message{
		{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: user}},
		},
	}
	text, usage, _, _, err := c.converseRaw(ctx, system, messages, nil, o)
	return text, usage, err
}

// ConverseResult holds the full output from a ConverseMessages call.
type ConverseResult struct {
	Text       string
	Usage      Usage
	StopReason types.StopReason
	Content    []types.ContentBlock
}

// ConverseMessages sends a full message history with optional tool config.
func (c *Client) ConverseMessages(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts ...inference.ConverseOption) (*ConverseResult, error) {
	o := inference.Apply(opts)
	text, usage, stop, content, err := c.converseRaw(ctx, system, messages, toolConfig, o)
	if err != nil {
		return nil, err
	}
	return &ConverseResult{
		Text:       text,
		Usage:      usage,
		StopReason: stop,
		Content:    content,
	}, nil
}

func (c *Client) converseRaw(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts inference.ConverseOptions) (string, Usage, types.StopReason, []types.ContentBlock, error) {
	var (
		text    string
		usage   Usage
		stop    types.StopReason
		content []types.ContentBlock
	)
	err := c.retryThrottled(ctx, func() error {
		var err error
		text, usage, stop, content, err = c.converseRawOnce(ctx, system, messages, toolConfig, opts)
		return err
	})
	if err != nil {
		return text, usage, stop, content, err
	}
	return text, usage, stop, content, nil
}

func (c *Client) converseRawOnce(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts inference.ConverseOptions) (string, Usage, types.StopReason, []types.ContentBlock, error) {
	maxTokens := int32(defaultMaxTokens)
	if opts.MaxTokens > 0 {
		maxTokens = opts.MaxTokens
	}
	if opts.ThinkingBudget > 0 {
		maxTokens += opts.ThinkingBudget
	}
	input := &bedrockruntime.ConverseInput{
		ModelId:  &c.model,
		System:   systemBlocks(system),
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

// retryThrottled runs fn with rate limiting and retries on throttling errors.
// Non-throttling errors (including nil) are returned immediately.
func (c *Client) retryThrottled(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := range maxRetries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.throttle:
		}
		err := fn()
		if err == nil || !isThrottling(err) {
			return err
		}
		lastErr = err
		backoff := backoffDuration(attempt)
		fmt.Fprintf(os.Stderr, "  throttled (attempt %d/%d), backing off %s\n", attempt+1, maxRetries, backoff.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return fmt.Errorf("throttled after %d retries: %w", maxRetries, lastErr)
}

// systemBlocks builds the system content blocks with a cache point after the
// text. The cache point tells Bedrock to cache the system prompt prefix so
// repeated calls with the same soul skip re-processing input tokens.
func systemBlocks(system string) []types.SystemContentBlock {
	return []types.SystemContentBlock{
		&types.SystemContentBlockMemberText{Value: system},
		&types.SystemContentBlockMemberCachePoint{Value: types.CachePointBlock{
			Type: types.CachePointTypeDefault,
		}},
	}
}

// StreamFunc receives text deltas as they arrive from the model.
type StreamFunc func(delta string)

// ConverseMessagesStream sends a full message history and streams text deltas
// through fn. Falls back to non-streaming Converse if the runtime doesn't
// support ConverseStream. Returns the complete result for session bookkeeping.
func (c *Client) ConverseMessagesStream(ctx context.Context, system string, messages []types.Message, fn StreamFunc, opts ...inference.ConverseOption) (*ConverseResult, error) {
	sr, ok := c.runtime.(StreamingRuntime)
	if !ok {
		// Fallback: non-streaming path (test mocks, etc.)
		result, err := c.ConverseMessages(ctx, system, messages, nil, opts...)
		if err != nil {
			return nil, err
		}
		if fn != nil {
			fn(result.Text)
		}
		return result, nil
	}

	o := inference.Apply(opts)
	maxTokens := int32(defaultMaxTokens)
	if o.MaxTokens > 0 {
		maxTokens = o.MaxTokens
	}
	if o.ThinkingBudget > 0 {
		maxTokens += o.ThinkingBudget
	}

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  &c.model,
		System:   systemBlocks(system),
		Messages: messages,
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aws.Int32(maxTokens),
		},
	}

	var result *ConverseResult
	err := c.retryThrottled(ctx, func() error {
		var err error
		result, err = c.converseStreamOnce(ctx, sr, input, fn)
		return err
	})
	return result, err
}

func (c *Client) converseStreamOnce(ctx context.Context, sr StreamingRuntime, input *bedrockruntime.ConverseStreamInput, fn StreamFunc) (*ConverseResult, error) {
	out, err := sr.ConverseStream(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("converse stream failed: %w", err)
	}
	stream := out.GetStream()
	defer stream.Close()

	var text strings.Builder
	var usage Usage
	var stopReason types.StopReason
	var content []types.ContentBlock

	for event := range stream.Events() {
		switch ev := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if td, ok := ev.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				text.WriteString(td.Value)
				if fn != nil {
					fn(td.Value)
				}
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stopReason = ev.Value.StopReason
		case *types.ConverseStreamOutputMemberMetadata:
			if ev.Value.Usage != nil {
				if ev.Value.Usage.InputTokens != nil {
					usage.InputTokens = int(*ev.Value.Usage.InputTokens)
				}
				if ev.Value.Usage.OutputTokens != nil {
					usage.OutputTokens = int(*ev.Value.Usage.OutputTokens)
				}
			}
			usage.Cost_ = float64(usage.InputTokens)*c.pricing.inputPerToken + float64(usage.OutputTokens)*c.pricing.outputPerToken
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("stream error: %w", err)
	}

	fullText := text.String()
	content = append(content, &types.ContentBlockMemberText{Value: fullText})

	if stopReason == types.StopReasonMaxTokens {
		return &ConverseResult{Text: fullText, Usage: usage, StopReason: stopReason, Content: content},
			fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}

	return &ConverseResult{
		Text:       fullText,
		Usage:      usage,
		StopReason: stopReason,
		Content:    content,
	}, nil
}
