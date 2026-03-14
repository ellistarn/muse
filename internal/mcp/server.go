package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/prompts"
)

// NewServer creates an MCP server with an ask tool.
// The server holds a single conversation session — the MCP connection is the session.
func NewServer(m *muse.Muse) *server.MCPServer {
	srv := server.NewMCPServer("muse", "0.1.0", server.WithToolCapabilities(false))
	var sessionID string
	srv.AddTool(
		mcp.NewTool("ask",
			mcp.WithDescription(prompts.Tool),
			mcp.WithString("question", mcp.Required(), mcp.Description("The question to ask")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			question, err := req.RequireString("question")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := m.Ask(ctx, muse.AskInput{
				Question:  question,
				SessionID: sessionID,
			})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to ask: %v", err)), nil
			}
			sessionID = result.SessionID
			log.Printf("tokens: %d in / %d out · $%.4f\n",
				result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.Cost())
			return mcp.NewToolResultText(result.Response), nil
		},
	)
	return srv
}
