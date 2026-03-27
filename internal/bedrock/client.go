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
	"sync"
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
)

// Usage is an alias for inference.Usage so callers don't need to import both packages.
type Usage = inference.Usage

// StreamFunc receives streaming deltas (text or thinking) as they arrive.
type StreamFunc = inference.StreamFunc

type modelPricing = inference.Pricing

// Bedrock on-demand pricing per token, keyed by model family substring.
// https://aws.amazon.com/bedrock/pricing/
var pricingTable = map[string]modelPricing{
	"claude-sonnet-4": {InputPerToken: 3.0 / 1_000_000, OutputPerToken: 15.0 / 1_000_000},
	"claude-opus-4":   {InputPerToken: 5.0 / 1_000_000, OutputPerToken: 25.0 / 1_000_000},
}

// lookupPricing finds pricing by matching a model family key against the full
// Bedrock model ID. Returns zero pricing if no match is found.
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

// Client wraps Bedrock's Converse API with adaptive rate limiting and retry.
type Client struct {
	runtime  Runtime
	model    string
	pricing  modelPricing
	throttle chan struct{} // token bucket: one token per request slot

	// Adaptive rate state
	rateMu      sync.Mutex
	ratePerSec  float64   // current target rate
	successes   int       // consecutive successes since last 429
	lastBackoff time.Time // last time we halved the rate (cooldown)
}

const (
	maxRetries  = 5
	baseBackoff = 2 * time.Second
	maxBackoff  = 60 * time.Second

	// Adaptive rate limiter parameters
	initialRate     = 20.0            // starting requests per second
	minRate         = 1.0             // floor
	maxRate         = 50.0            // ceiling
	backoffCooldown = 5 * time.Second // don't halve rate more than once per cooldown
	growthThreshold = 10              // consecutive successes before rate increase
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
		runtime:    bedrockruntime.NewFromConfig(cfg),
		model:      model,
		pricing:    lookupPricing(model),
		throttle:   make(chan struct{}, int(maxRate)),
		ratePerSec: initialRate,
	}
	// Start the token refiller: adds request tokens at the adaptive rate.
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

// ---------------------------------------------------------------------------
// inference.Client interface — provider-agnostic methods
// ---------------------------------------------------------------------------

// ConverseMessages sends a multi-turn conversation using provider-agnostic messages.
func (c *Client) ConverseMessages(ctx context.Context, system string, messages []inference.Message, opts ...inference.ConverseOption) (*inference.Response, error) {
	o := inference.Apply(opts)
	text, usage, _, err := c.converseRaw(ctx, system, toBedrockMessages(messages), nil, o)
	resp := &inference.Response{Text: text, Usage: usage}
	if err != nil {
		return resp, err
	}
	return resp, nil
}

// ConverseMessagesStream sends a multi-turn conversation and streams text deltas.
// Falls back to non-streaming Converse if the runtime doesn't support streaming.
func (c *Client) ConverseMessagesStream(ctx context.Context, system string, messages []inference.Message, fn StreamFunc, opts ...inference.ConverseOption) (*inference.Response, error) {
	sr, ok := c.runtime.(StreamingRuntime)
	if !ok {
		// Fallback: non-streaming path (test mocks, etc.)
		result, err := c.ConverseMessages(ctx, system, messages, opts...)
		if result == nil {
			return nil, err
		}
		if fn != nil {
			fn(inference.StreamDelta{Text: result.Text})
		}
		return result, err
	}

	o := inference.Apply(opts)
	maxTokens := int32(inference.DefaultMaxTokens)
	if o.MaxTokens > 0 {
		maxTokens = o.MaxTokens
	}
	if o.ThinkingBudget > 0 {
		maxTokens += o.ThinkingBudget
	}

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  &c.model,
		System:   systemBlocks(system),
		Messages: toBedrockMessages(messages),
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aws.Int32(maxTokens),
		},
	}
	if o.ThinkingBudget > 0 {
		input.AdditionalModelRequestFields = document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": o.ThinkingBudget,
			},
		})
	}

	var result *inference.Response
	err := c.retryThrottled(ctx, func() error {
		var err error
		result, err = c.converseStreamOnce(ctx, sr, input, fn)
		return err
	})
	return result, err
}

// ---------------------------------------------------------------------------
// Internal Bedrock implementation
// ---------------------------------------------------------------------------

// toBedrockMessages converts provider-agnostic messages to Bedrock types.
func toBedrockMessages(messages []inference.Message) []types.Message {
	out := make([]types.Message, len(messages))
	for i, m := range messages {
		role := types.ConversationRoleUser
		if m.Role == "assistant" {
			role = types.ConversationRoleAssistant
		}
		out[i] = types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Content}},
		}
	}
	return out
}

func (c *Client) converseRaw(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts inference.ConverseOptions) (string, Usage, types.StopReason, error) {
	var (
		text  string
		usage Usage
		stop  types.StopReason
	)
	err := c.retryThrottled(ctx, func() error {
		var err error
		text, usage, stop, err = c.converseRawOnce(ctx, system, messages, toolConfig, opts)
		return err
	})
	return text, usage, stop, err
}

func (c *Client) converseRawOnce(ctx context.Context, system string, messages []types.Message, toolConfig *types.ToolConfiguration, opts inference.ConverseOptions) (string, Usage, types.StopReason, error) {
	maxTokens := int32(inference.DefaultMaxTokens)
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
		return "", Usage{}, "", fmt.Errorf("converse failed: %w", err)
	}
	usage := c.extractUsage(out)
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", usage, out.StopReason, nil
	}
	var text strings.Builder
	for _, block := range msg.Value.Content {
		if tb, ok := block.(*types.ContentBlockMemberText); ok {
			text.WriteString(tb.Value)
		}
	}
	textValue := text.String()
	if out.StopReason == types.StopReasonMaxTokens {
		return textValue, usage, out.StopReason, fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}
	return textValue, usage, out.StopReason, nil
}

func (c *Client) extractUsage(out *bedrockruntime.ConverseOutput) Usage {
	var in, outt int
	if out.Usage != nil {
		if out.Usage.InputTokens != nil {
			in = int(*out.Usage.InputTokens)
		}
		if out.Usage.OutputTokens != nil {
			outt = int(*out.Usage.OutputTokens)
		}
	}
	return inference.NewUsage(in, outt, c.pricing.ComputeCost(in, outt))
}

func (c *Client) converseStreamOnce(ctx context.Context, sr StreamingRuntime, input *bedrockruntime.ConverseStreamInput, fn StreamFunc) (*inference.Response, error) {
	out, err := sr.ConverseStream(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("converse stream failed: %w", err)
	}
	stream := out.GetStream()
	defer stream.Close()

	var text strings.Builder
	var usage Usage
	var stopReason types.StopReason

	for event := range stream.Events() {
		switch ev := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			switch delta := ev.Value.Delta.(type) {
			case *types.ContentBlockDeltaMemberText:
				text.WriteString(delta.Value)
				if fn != nil {
					fn(inference.StreamDelta{Text: delta.Value})
				}
			case *types.ContentBlockDeltaMemberReasoningContent:
				if td, ok := delta.Value.(*types.ReasoningContentBlockDeltaMemberText); ok {
					if fn != nil {
						fn(inference.StreamDelta{Text: td.Value, Thinking: true})
					}
				}
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			stopReason = ev.Value.StopReason
		case *types.ConverseStreamOutputMemberMetadata:
			var in, out int
			if ev.Value.Usage != nil {
				if ev.Value.Usage.InputTokens != nil {
					in = int(*ev.Value.Usage.InputTokens)
				}
				if ev.Value.Usage.OutputTokens != nil {
					out = int(*ev.Value.Usage.OutputTokens)
				}
			}
			usage = inference.NewUsage(in, out, c.pricing.ComputeCost(in, out))
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("stream error: %w", err)
	}

	fullText := text.String()

	if stopReason == types.StopReasonMaxTokens {
		return &inference.Response{Text: fullText, Usage: usage},
			fmt.Errorf("response truncated: hit max token limit (%d output tokens)", usage.OutputTokens)
	}

	return &inference.Response{Text: fullText, Usage: usage}, nil
}

// ---------------------------------------------------------------------------
// Rate limiting and retry
// ---------------------------------------------------------------------------

func (c *Client) refillTokens(ctx context.Context) {
	interval := time.Second / time.Duration(c.currentRate())
	ticker := time.NewTicker(interval)
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
			// Adjust ticker if rate changed
			newInterval := time.Second / time.Duration(c.currentRate())
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
			}
		}
	}
}

func (c *Client) currentRate() float64 {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	return c.ratePerSec
}

func (c *Client) onThrottle() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	c.successes = 0
	if time.Since(c.lastBackoff) < backoffCooldown {
		return // already backed off recently
	}
	c.lastBackoff = time.Now()
	c.ratePerSec = max(c.ratePerSec/2, minRate)
	fmt.Fprintf(os.Stderr, "  rate → %.0f req/s\n", c.ratePerSec)
}

func (c *Client) onSuccess() {
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	c.successes++
	if c.successes >= growthThreshold {
		c.successes = 0
		c.ratePerSec = min(c.ratePerSec+1, maxRate)
	}
}

func isThrottling(err error) bool {
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 429 {
		return true
	}
	return strings.Contains(err.Error(), "ThrottlingException") || strings.Contains(err.Error(), "Too many tokens")
}

func backoffDuration(attempt int) time.Duration {
	backoff := float64(baseBackoff) * math.Pow(2, float64(attempt))
	if backoff > float64(maxBackoff) {
		backoff = float64(maxBackoff)
	}
	jitter := 0.5 + rand.Float64()*0.5
	return time.Duration(backoff * jitter)
}

func (c *Client) retryThrottled(ctx context.Context, fn func() error) error {
	var lastErr error
	for attempt := range maxRetries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.throttle:
		}
		err := fn()
		if err == nil {
			c.onSuccess()
			return nil
		}
		if !isThrottling(err) {
			return err
		}
		c.onThrottle()
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

func systemBlocks(system string) []types.SystemContentBlock {
	return []types.SystemContentBlock{
		&types.SystemContentBlockMemberText{Value: system},
		&types.SystemContentBlockMemberCachePoint{Value: types.CachePointBlock{
			Type: types.CachePointTypeDefault,
		}},
	}
}
