package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	return newMCPClientWithOpts(t, document, nil, responses...)
}

// newMCPClientWithOpts creates an in-process MCP client, passing opts through to Muse.
func newMCPClientWithOpts(t *testing.T, document string, opts []museinternal.Option, responses ...bedrockruntime.ConverseOutput) *client.Client {
	t.Helper()
	runtime := &mockRuntime{responses: responses}
	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := museinternal.New(bedrockClient, document, opts...)
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

func TestMCP_SessionPersistedToDisk(t *testing.T) {
	dir := t.TempDir()
	c := newMCPClientWithOpts(t, "Be concise.",
		[]museinternal.Option{museinternal.WithSessionsDir(dir)},
		textResponse("Persisted answer."),
	)

	result := callAsk(t, c, "hello")
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}

	// Verify a session file was written to disk.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read sessions dir: %v", err)
	}
	var jsonFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			jsonFiles = append(jsonFiles, e.Name())
		}
	}
	if len(jsonFiles) != 1 {
		t.Fatalf("expected 1 session file, got %d: %v", len(jsonFiles), jsonFiles)
	}

	// Verify the session contains the conversation.
	data, err := os.ReadFile(filepath.Join(dir, jsonFiles[0]))
	if err != nil {
		t.Fatalf("failed to read session file: %v", err)
	}
	var session struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatalf("failed to parse session: %v", err)
	}
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(session.Messages))
	}
	if session.Messages[0].Role != "user" || session.Messages[0].Content != "hello" {
		t.Errorf("first message = %+v, want user/hello", session.Messages[0])
	}
	if session.Messages[1].Role != "assistant" || session.Messages[1].Content != "Persisted answer." {
		t.Errorf("second message = %+v, want assistant/Persisted answer.", session.Messages[1])
	}

	// MCP sessions should NOT create a "latest" pointer (that's a CLI concern).
	if _, err := os.Stat(filepath.Join(dir, "latest")); !os.IsNotExist(err) {
		t.Error("MCP session should not create a 'latest' pointer file")
	}
}

func TestMCP_DoesNotResumeAskSession(t *testing.T) {
	// Simulate a prior "muse ask" session by writing a session file and latest pointer.
	dir := t.TempDir()
	askSession := struct {
		ID       string `json:"id"`
		System   string `json:"system"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{
		ID:     "ask-session-id",
		System: "old system prompt",
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{Role: "user", Content: "prior ask question"},
			{Role: "assistant", Content: "prior ask answer"},
		},
	}
	data, err := json.Marshal(askSession)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ask-session-id.json"), data, 0o644); err != nil {
		t.Fatalf("failed to write session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "latest"), []byte("ask-session-id"), 0o644); err != nil {
		t.Fatalf("failed to write latest: %v", err)
	}

	// Create an MCP client with the same sessions dir. The MCP server should
	// start a fresh session, NOT resume the ask session.
	runtime := &mockRuntime{responses: []bedrockruntime.ConverseOutput{
		textResponse("Fresh MCP answer."),
	}}
	ctx := context.Background()
	bedrockClient := bedrock.NewClientWithRuntime(ctx, runtime)
	m := museinternal.New(bedrockClient, "test doc", museinternal.WithSessionsDir(dir))
	srv := musemcp.NewServer(m)

	c, err := client.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.Start(ctx); err != nil {
		t.Fatalf("failed to start: %v", err)
	}
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		t.Fatalf("failed to initialize: %v", err)
	}

	result := callAsk(t, c, "new MCP question")
	if result.IsError {
		t.Fatalf("expected success, got error: %v", result.Content)
	}

	// The MCP call should have sent only 1 message (the new question),
	// NOT 3 (prior ask history + new question).
	if len(runtime.calls) != 1 {
		t.Fatalf("expected 1 Bedrock call, got %d", len(runtime.calls))
	}
	if runtime.calls[0].messages != 1 {
		t.Errorf("Bedrock received %d messages, want 1 (fresh session); got ask session history leak", runtime.calls[0].messages)
	}

	// The "latest" pointer should still point to the ask session, not the MCP session.
	latestData, err := os.ReadFile(filepath.Join(dir, "latest"))
	if err != nil {
		t.Fatalf("failed to read latest: %v", err)
	}
	if string(latestData) != "ask-session-id" {
		t.Errorf("latest = %q, want %q; MCP session overwrote the CLI latest pointer", string(latestData), "ask-session-id")
	}
}
