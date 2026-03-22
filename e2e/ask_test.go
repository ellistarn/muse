package e2e

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/muse"
)

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
		return nil, nil
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

func TestAskWithSoul(t *testing.T) {
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("Use kebab-case for your file names."),
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := muse.New(bedrockClient, "Use kebab-case for file names. Always wrap errors with context.")

	result, err := m.Ask(ctx, muse.AskInput{Question: "how should I name files?"})
	if err != nil {
		t.Fatalf("Ask() error: %v", err)
	}
	if result.Response != "Use kebab-case for your file names." {
		t.Errorf("response = %q, want %q", result.Response, "Use kebab-case for your file names.")
	}
	if result.SessionID == "" {
		t.Error("expected sessionId to be set")
	}

	// Single Bedrock call
	if len(runtime.calls) != 1 {
		t.Fatalf("Bedrock calls = %d, want 1", len(runtime.calls))
	}
	// Soul content should be in system prompt
	if !strings.Contains(runtime.calls[0].system, "kebab-case") {
		t.Error("system prompt missing muse content")
	}
	if !strings.Contains(runtime.calls[0].system, "wrap errors") {
		t.Error("system prompt missing muse content about error handling")
	}
	// Single user message
	if runtime.calls[0].messages != 1 {
		t.Errorf("messages = %d, want 1", runtime.calls[0].messages)
	}
}

func TestAskEmptyMuse(t *testing.T) {
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("I don't have any knowledge to draw on for that."),
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := muse.New(bedrockClient, "")

	result, err := m.Ask(ctx, muse.AskInput{Question: "how do you handle errors?"})
	if err != nil {
		t.Fatalf("Ask() error: %v", err)
	}
	if result.Response != "I don't have any knowledge to draw on for that." {
		t.Errorf("response = %q", result.Response)
	}

	if len(runtime.calls) != 1 {
		t.Fatalf("Bedrock calls = %d, want 1", len(runtime.calls))
	}
	if !strings.Contains(runtime.calls[0].system, "No muse available") {
		t.Error("system prompt should indicate no muse available")
	}
}

func TestAskMultiTurn(t *testing.T) {
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("I'd need to see the code to give useful guidance. Can you share the file?"),
		textResponse("That function looks fine, but I'd extract the validation into a helper."),
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := muse.New(bedrockClient, "Keep functions small.")

	// Turn 1: initial question
	r1, err := m.Ask(ctx, muse.AskInput{Question: "Is my handler well structured?"})
	if err != nil {
		t.Fatalf("Turn 1 error: %v", err)
	}
	if r1.SessionID == "" {
		t.Fatal("expected sessionId after turn 1")
	}
	if !strings.Contains(r1.Response, "see the code") {
		t.Errorf("turn 1 response = %q", r1.Response)
	}

	// Turn 2: provide context
	r2, err := m.Ask(ctx, muse.AskInput{
		SessionID: r1.SessionID,
		Question:  "func handler(w http.ResponseWriter, r *http.Request) { validate(r); process(r); respond(w) }",
	})
	if err != nil {
		t.Fatalf("Turn 2 error: %v", err)
	}
	if !strings.Contains(r2.Response, "extract the validation") {
		t.Errorf("turn 2 response = %q", r2.Response)
	}

	// Verify Bedrock received the full history on turn 2
	if len(runtime.calls) != 2 {
		t.Fatalf("Bedrock calls = %d, want 2", len(runtime.calls))
	}
	// Turn 2 should have 3 messages: user question, assistant response, user follow-up
	if runtime.calls[1].messages != 3 {
		t.Errorf("turn 2 messages = %d, want 3", runtime.calls[1].messages)
	}
}

func TestAskIndependentSessions(t *testing.T) {
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("Answer to question A"),
		textResponse("Answer to question B"),
		textResponse("Follow-up to A with context"),
	}}

	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := muse.New(bedrockClient, "Be concise.")

	// Start two independent conversations
	r1, err := m.Ask(ctx, muse.AskInput{Question: "Question A"})
	if err != nil {
		t.Fatalf("Session A error: %v", err)
	}
	r2, err := m.Ask(ctx, muse.AskInput{Question: "Question B"})
	if err != nil {
		t.Fatalf("Session B error: %v", err)
	}

	if r1.SessionID == r2.SessionID {
		t.Error("independent sessions should have different IDs")
	}

	// Resume session A — should not contain session B's context
	r3, err := m.Ask(ctx, muse.AskInput{SessionID: r1.SessionID, Question: "More about A"})
	if err != nil {
		t.Fatalf("Resume A error: %v", err)
	}
	if r3.Response != "Follow-up to A with context" {
		t.Errorf("resume response = %q", r3.Response)
	}

	// Session A resume should have 3 messages (user, assistant, user follow-up)
	if runtime.calls[2].messages != 3 {
		t.Errorf("resume messages = %d, want 3", runtime.calls[2].messages)
	}
}

func TestAskExpiredSession(t *testing.T) {
	runtime := &mockRuntime{}
	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := muse.New(bedrockClient, "")

	_, err := m.Ask(ctx, muse.AskInput{SessionID: "nonexistent", Question: "hello"})
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}
