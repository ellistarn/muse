package e2e

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ellistarn/shade/internal/bedrock"
	"github.com/ellistarn/shade/internal/shade"
)

// mockS3 implements skill.S3API with in-memory skill files.
type mockS3 struct {
	skills map[string]string // key -> content
}

func (m *mockS3) ListObjectsV2(_ context.Context, params *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	prefix := aws.ToString(params.Prefix)
	var objects []s3types.Object
	for key := range m.skills {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, s3types.Object{Key: aws.String(key)})
		}
	}
	return &s3.ListObjectsV2Output{
		Contents:    objects,
		IsTruncated: aws.Bool(false),
	}, nil
}

func (m *mockS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	key := aws.ToString(params.Key)
	content, ok := m.skills[key]
	if !ok {
		return nil, fmt.Errorf("NoSuchKey: %s", key)
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(content)),
	}, nil
}

// mockRuntime implements bedrock.Runtime with canned responses.
type mockRuntime struct {
	calls     []converseCall
	responses []bedrockruntime.ConverseOutput
	callIndex int
}

type converseCall struct {
	system   string
	messages int
}

func (m *mockRuntime) Converse(_ context.Context, params *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	var system string
	for _, block := range params.System {
		if tb, ok := block.(*brtypes.SystemContentBlockMemberText); ok {
			system = tb.Value
		}
	}
	m.calls = append(m.calls, converseCall{system: system, messages: len(params.Messages)})
	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("unexpected call %d", m.callIndex)
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return &resp, nil
}

func textResponse(text string) bedrockruntime.ConverseOutput {
	return bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonEndTurn,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberText{Value: text},
				},
			},
		},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(100),
			OutputTokens: aws.Int32(50),
		},
	}
}

func toolUseResponse(toolUseID, skillName string) bedrockruntime.ConverseOutput {
	return bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonToolUse,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{
					&brtypes.ContentBlockMemberToolUse{
						Value: brtypes.ToolUseBlock{
							ToolUseId: aws.String(toolUseID),
							Name:      aws.String("read_skill"),
							Input:     document.NewLazyDocument(map[string]any{"name": skillName}),
						},
					},
				},
			},
		},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(80),
			OutputTokens: aws.Int32(20),
		},
	}
}

func multiToolUseResponse(skills ...string) bedrockruntime.ConverseOutput {
	var blocks []brtypes.ContentBlock
	for i, name := range skills {
		blocks = append(blocks, &brtypes.ContentBlockMemberToolUse{
			Value: brtypes.ToolUseBlock{
				ToolUseId: aws.String(fmt.Sprintf("tool-%d", i)),
				Name:      aws.String("read_skill"),
				Input:     document.NewLazyDocument(map[string]any{"name": name}),
			},
		})
	}
	return bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonToolUse,
		Output: &brtypes.ConverseOutputMemberMessage{
			Value: brtypes.Message{
				Role:    brtypes.ConversationRoleAssistant,
				Content: blocks,
			},
		},
		Usage: &brtypes.TokenUsage{
			InputTokens:  aws.Int32(80),
			OutputTokens: aws.Int32(20),
		},
	}
}

const skillFileTemplate = `---
name: %s
description: %s
---

%s`

func TestAdviseWithSkillLookup(t *testing.T) {
	s3Client := &mockS3{skills: map[string]string{
		"skills/naming-conventions/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Naming Conventions", "File and commit naming preferences.", "Use kebab-case for file names."),
		"skills/error-handling/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Error Handling", "How to structure error returns.", "Always wrap errors with context."),
	}}

	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		toolUseResponse("tool-1", "naming-conventions"),
		textResponse("I found the naming skill."),           // internal reasoning
		textResponse("Use kebab-case for your file names."), // synthesis
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	s := shade.NewForTest(s3Client, bedrockClient, "test-bucket")

	answer, err := s.Advise(ctx, "how should I name files?")
	if err != nil {
		t.Fatalf("Advise() error: %v", err)
	}
	if answer != "Use kebab-case for your file names." {
		t.Errorf("answer = %q, want %q", answer, "Use kebab-case for your file names.")
	}

	// 3 Bedrock calls: tool request + tool result response + synthesis
	if len(runtime.calls) != 3 {
		t.Fatalf("Bedrock calls = %d, want 3", len(runtime.calls))
	}
	// First call should have the catalog in system prompt
	if !strings.Contains(runtime.calls[0].system, "naming-conventions") {
		t.Error("first call system prompt missing skill catalog")
	}
	if !strings.Contains(runtime.calls[0].system, "error-handling") {
		t.Error("first call system prompt missing error-handling in catalog")
	}
	// Message counts: 1, 3 (user + assistant tool_use + user tool_result),
	// 5 (+ assistant text + user synthesis prompt)
	if runtime.calls[0].messages != 1 {
		t.Errorf("first call messages = %d, want 1", runtime.calls[0].messages)
	}
	if runtime.calls[1].messages != 3 {
		t.Errorf("second call messages = %d, want 3", runtime.calls[1].messages)
	}
	if runtime.calls[2].messages != 5 {
		t.Errorf("third call messages = %d, want 5", runtime.calls[2].messages)
	}
}

func TestAdviseNoSkillsNeeded(t *testing.T) {
	s3Client := &mockS3{skills: map[string]string{
		"skills/naming-conventions/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Naming Conventions", "File and commit naming preferences.", "Use kebab-case."),
	}}

	// LLM answers directly without calling read_skill
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("I can help with that."),  // internal reasoning
		textResponse("Hello! How can I help?"), // synthesis
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	s := shade.NewForTest(s3Client, bedrockClient, "test-bucket")

	answer, err := s.Advise(ctx, "hello")
	if err != nil {
		t.Fatalf("Advise() error: %v", err)
	}
	if answer != "Hello! How can I help?" {
		t.Errorf("answer = %q, want %q", answer, "Hello! How can I help?")
	}

	// Two Bedrock calls: initial response + synthesis
	if len(runtime.calls) != 2 {
		t.Fatalf("Bedrock calls = %d, want 2", len(runtime.calls))
	}
	// Catalog should still be in system prompt
	if !strings.Contains(runtime.calls[0].system, "naming-conventions") {
		t.Error("system prompt missing skill catalog")
	}
}

func TestAdviseEmptyCatalog(t *testing.T) {
	s3Client := &mockS3{skills: map[string]string{}}

	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("No skills match this question."),             // internal reasoning
		textResponse("I don't have any relevant skills for that."), // synthesis
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	s := shade.NewForTest(s3Client, bedrockClient, "test-bucket")

	answer, err := s.Advise(ctx, "how do you handle errors?")
	if err != nil {
		t.Fatalf("Advise() error: %v", err)
	}
	if answer != "I don't have any relevant skills for that." {
		t.Errorf("answer = %q", answer)
	}

	if len(runtime.calls) != 2 {
		t.Fatalf("Bedrock calls = %d, want 2", len(runtime.calls))
	}
	if !strings.Contains(runtime.calls[0].system, "No skills are currently available") {
		t.Error("system prompt should indicate no skills available")
	}
}

func TestAdviseMultipleSkills(t *testing.T) {
	s3Client := &mockS3{skills: map[string]string{
		"skills/naming-conventions/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Naming Conventions", "File naming preferences.", "Use kebab-case."),
		"skills/error-handling/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Error Handling", "Error return patterns.", "Wrap errors with context."),
		"skills/testing/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Testing", "Test structure preferences.", "One test per core interaction."),
	}}

	// LLM requests two skills in one round
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		multiToolUseResponse("naming-conventions", "error-handling"),
		textResponse("I found both skills."),                         // internal reasoning
		textResponse("Use kebab-case and wrap errors with context."), // synthesis
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	s := shade.NewForTest(s3Client, bedrockClient, "test-bucket")

	answer, err := s.Advise(ctx, "what are your coding conventions?")
	if err != nil {
		t.Fatalf("Advise() error: %v", err)
	}
	if answer != "Use kebab-case and wrap errors with context." {
		t.Errorf("answer = %q", answer)
	}

	if len(runtime.calls) != 3 {
		t.Fatalf("Bedrock calls = %d, want 3", len(runtime.calls))
	}
	// Third call (synthesis) should have 5 messages: user + assistant (2 tool_use blocks) + user (2 tool_result blocks) + assistant text + user synthesis
	if runtime.calls[2].messages != 5 {
		t.Errorf("third call messages = %d, want 5", runtime.calls[2].messages)
	}
}

func TestAdviseMultiRoundToolUse(t *testing.T) {
	s3Client := &mockS3{skills: map[string]string{
		"skills/error-handling/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Error Handling", "Error return patterns.", "Wrap errors with context. See also: logging."),
		"skills/logging/SKILL.md": fmt.Sprintf(skillFileTemplate,
			"Logging", "Logging conventions.", "Use structured logging with slog."),
	}}

	// Round 1: LLM reads error-handling
	// Round 2: after seeing "see also: logging", LLM reads logging
	// Round 3: LLM gives internal answer, then synthesis produces final answer
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		toolUseResponse("tool-1", "error-handling"),
		toolUseResponse("tool-2", "logging"),
		textResponse("I've gathered both skills."),                           // internal reasoning
		textResponse("Wrap errors with context and use structured logging."), // synthesis
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	s := shade.NewForTest(s3Client, bedrockClient, "test-bucket")

	answer, err := s.Advise(ctx, "how should I handle errors?")
	if err != nil {
		t.Fatalf("Advise() error: %v", err)
	}
	if answer != "Wrap errors with context and use structured logging." {
		t.Errorf("answer = %q", answer)
	}

	// 4 Bedrock calls: tool1 + tool2 + internal answer + synthesis
	if len(runtime.calls) != 4 {
		t.Fatalf("Bedrock calls = %d, want 4", len(runtime.calls))
	}
	// Message counts grow: 1, 3, 5, then +2 for synthesis = 7
	if runtime.calls[0].messages != 1 {
		t.Errorf("call 0 messages = %d, want 1", runtime.calls[0].messages)
	}
	if runtime.calls[1].messages != 3 {
		t.Errorf("call 1 messages = %d, want 3", runtime.calls[1].messages)
	}
	if runtime.calls[2].messages != 5 {
		t.Errorf("call 2 messages = %d, want 5", runtime.calls[2].messages)
	}
	if runtime.calls[3].messages != 7 {
		t.Errorf("call 3 messages = %d, want 7", runtime.calls[3].messages)
	}
}
