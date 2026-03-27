package e2e

import (
	"context"
	"strings"
	"testing"

	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/ellistarn/muse/internal/bedrock"
	musemcp "github.com/ellistarn/muse/internal/mcp"
	museinternal "github.com/ellistarn/muse/internal/muse"
)

// newMCPClient creates an in-process MCP client backed by a Muse with canned responses.
func newMCPClient(t *testing.T, document string, responses ...bedrockruntime.ConverseOutput) *client.Client {
	t.Helper()
	runtime := &mockRuntime{responses: responses}
	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := museinternal.New(bedrockClient, document)
	srv := musemcp.NewServer(m)

	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("failed to create in-process client: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start client: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("failed to initialize: %v", err)
	}
	return c
}

func callAsk(t *testing.T, c *client.Client, question string) *mcp.CallToolResult {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = "ask"
	req.Params.Arguments = map[string]any{"question": question}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("CallTool returned protocol error: %v", err)
	}
	return result
}

func TestMCP_AskReturnsResponse(t *testing.T) {
	c := newMCPClient(t, "Use kebab-case for files.",
		textResponse("Use kebab-case."),
	)
	result := callAsk(t, c, "how should I name files?")
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}
	text := result.Content[0].(mcp.TextContent).Text
	if text != "Use kebab-case." {
		t.Errorf("response = %q, want %q", text, "Use kebab-case.")
	}
}

func TestMCP_ConversationContinuity(t *testing.T) {
	c := newMCPClient(t, "Be concise.",
		textResponse("First answer."),
		textResponse("Follow-up answer with context."),
	)

	// First call
	r1 := callAsk(t, c, "question one")
	if r1.IsError {
		t.Fatalf("turn 1 error: %v", r1.Content)
	}

	// Second call should reuse the Bedrock session (MCP server maintains sessionID)
	r2 := callAsk(t, c, "follow up")
	if r2.IsError {
		t.Fatalf("turn 2 error: %v", r2.Content)
	}
	text := r2.Content[0].(mcp.TextContent).Text
	if text != "Follow-up answer with context." {
		t.Errorf("turn 2 response = %q", text)
	}
}

func TestMCP_ErrorsReturnedAsToolResults(t *testing.T) {
	// Create a runtime that returns a non-throttling error from Bedrock.
	// Using a generic error (not ThrottlingException) avoids retry backoff.
	errRuntime := &errorRuntime{err: fmt.Errorf("model not available")}
	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, errRuntime)
	m := museinternal.New(bedrockClient, "test document")
	srv := musemcp.NewServer(m)

	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	defer c.Close()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("failed to initialize: %v", err)
	}

	// Call the tool — this should NOT return a protocol error
	req := mcp.CallToolRequest{}
	req.Params.Name = "ask"
	req.Params.Arguments = map[string]any{"question": "hello"}
	result, err := c.CallTool(ctx, req)
	// The key assertion: err should be nil (no protocol error)
	// The error should be in the result as IsError=true (regression for #30)
	if err != nil {
		t.Fatalf("got protocol error (regression #30): %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result error, got success")
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "failed to ask") {
		t.Errorf("error text = %q, want it to contain 'failed to ask'", text)
	}
}

func TestMCP_MissingQuestionReturnsToolError(t *testing.T) {
	c := newMCPClient(t, "test document")

	// Call without the required "question" argument
	req := mcp.CallToolRequest{}
	req.Params.Name = "ask"
	req.Params.Arguments = map[string]any{}
	result, err := c.CallTool(context.Background(), req)
	if err != nil {
		t.Fatalf("got protocol error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool result error for missing question")
	}
}

func TestMCP_ListToolsReturnsAsk(t *testing.T) {
	c := newMCPClient(t, "test document")
	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result.Tools))
	}
	if result.Tools[0].Name != "ask" {
		t.Errorf("tool name = %q, want %q", result.Tools[0].Name, "ask")
	}
}

// errorRuntime implements bedrock.Runtime but always returns an error.
type errorRuntime struct {
	err error
}

func (e *errorRuntime) Converse(_ context.Context, _ *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
	return nil, e.err
}
