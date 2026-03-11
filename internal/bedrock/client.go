package bedrock

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

const defaultModel = "us.anthropic.claude-sonnet-4-20250514-v1:0"

// Client wraps Bedrock's Converse API with extended thinking enabled.
type Client struct {
	runtime *bedrockruntime.Client
	model   string
}

func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-west-2"))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	model := os.Getenv("SHADE_MODEL")
	if model == "" {
		model = defaultModel
	}
	return &Client{
		runtime: bedrockruntime.NewFromConfig(cfg),
		model:   model,
	}, nil
}

// Converse sends a message with a system prompt and returns the text response.
// Extended thinking is enabled so the shade can reason through its answer.
func (c *Client) Converse(ctx context.Context, system, user string) (string, error) {
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
		}),
	})
	if err != nil {
		return "", fmt.Errorf("converse failed: %w", err)
	}
	return extractText(out), nil
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
