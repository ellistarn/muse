package mcp

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/ellistarn/shade/internal/shade"
	"github.com/ellistarn/shade/prompts"
)

// NewServer creates an MCP server with a single "ask" tool.
func NewServer(s *shade.Shade) *server.MCPServer {
	srv := server.NewMCPServer("shade", "0.1.0", server.WithToolCapabilities(false))
	srv.AddTool(
		mcp.NewTool("ask",
			mcp.WithDescription(prompts.Tool),
			mcp.WithString("question", mcp.Required(), mcp.Description("The question to ask")),
		),
		askHandler(s),
	)
	return srv
}

func askHandler(s *shade.Shade) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		question, err := req.RequireString("question")
		if err != nil {
			return nil, err
		}
		answer, err := s.Ask(ctx, question)
		if err != nil {
			return nil, fmt.Errorf("failed to ask: %w", err)
		}
		return mcp.NewToolResultText(answer), nil
	}
}
