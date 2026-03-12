package bedrock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const defaultModel = "us.anthropic.claude-opus-4-6-v1"

// Usage tracks token consumption from a Converse call.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	inputPricePerToken  float64
	outputPricePerToken float64
}

// Cost returns the estimated dollar cost for this usage.
func (u Usage) Cost() float64 {
	return float64(u.InputTokens)*u.inputPricePerToken + float64(u.OutputTokens)*u.outputPricePerToken
}

// Add combines two Usage values, preserving pricing from whichever has it.
func (u Usage) Add(other Usage) Usage {
	result := Usage{
		InputTokens:  u.InputTokens + other.InputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens,
	}
	if u.inputPricePerToken != 0 {
		result.inputPricePerToken = u.inputPricePerToken
		result.outputPricePerToken = u.outputPricePerToken
	} else {
		result.inputPricePerToken = other.inputPricePerToken
		result.outputPricePerToken = other.outputPricePerToken
	}
	return result
}

type modelPricing struct {
	inputPerToken  float64
	outputPerToken float64
}

// Bedrock on-demand pricing per token, keyed by model family substring.
// https://aws.amazon.com/bedrock/pricing/
var pricingTable = map[string]modelPricing{
	"claude-sonnet-4": {3.0 / 1_000_000, 15.0 / 1_000_000},
	"claude-opus-4-6": {6.0 / 1_000_000, 30.0 / 1_000_000},
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

// Client wraps Bedrock's Converse API with rate limiting and retry.
type Client struct {
	runtime  *bedrockruntime.Client
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

func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	model := os.Getenv("SHADE_MODEL")
	if model == "" {
		model = defaultModel
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
func (c *Client) Converse(ctx context.Context, system, user string) (string, Usage, error) {
	var lastErr error
	for attempt := range maxRetries {
		// Wait for a request token (rate limiting)
		select {
		case <-ctx.Done():
			return "", Usage{}, ctx.Err()
		case <-c.throttle:
		}

		text, usage, err := c.converse(ctx, system, user)
		if err == nil {
			return text, usage, nil
		}
		if !isThrottling(err) {
			return "", usage, err
		}
		lastErr = err
		backoff := backoffDuration(attempt)
		fmt.Printf("  throttled (attempt %d/%d), backing off %s\n", attempt+1, maxRetries, backoff.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", Usage{}, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return "", Usage{}, fmt.Errorf("throttled after %d retries: %w", maxRetries, lastErr)
}

func (c *Client) converse(ctx context.Context, system, user string) (string, Usage, error) {
	out, err := c.runtime.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: &c.model,
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: system},
		},
		Messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: user},
				},
			},
		},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aws.Int32(16000),
		},
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{
				"type":          "enabled",
				"budget_tokens": 10000,
			},
			// Anthropic recommends medium effort for Claude Opus 4.6
			"reasoning": map[string]any{
				"effort": "medium",
			},
		}),
	})
	if err != nil {
		return "", Usage{}, fmt.Errorf("converse failed: %w", err)
	}
	var usage Usage
	if out.Usage != nil {
		if out.Usage.InputTokens != nil {
			usage.InputTokens = int(*out.Usage.InputTokens)
		}
		if out.Usage.OutputTokens != nil {
			usage.OutputTokens = int(*out.Usage.OutputTokens)
		}
	}
	usage.inputPricePerToken = c.pricing.inputPerToken
	usage.outputPricePerToken = c.pricing.outputPerToken
	return extractText(out), usage, nil
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

func extractText(out *bedrockruntime.ConverseOutput) string {
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	for _, block := range msg.Value.Content {
		if tb, ok := block.(*types.ContentBlockMemberText); ok {
			return tb.Value
		}
	}
	return ""
}
