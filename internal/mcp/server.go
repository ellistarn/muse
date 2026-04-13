package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/prompts"
)

// flushInterval controls how often thinking tokens are flushed as log
// notifications to keep the MCP connection alive during long inference calls.
const flushInterval = 3 * time.Second

// NewServer creates an MCP server with an ask tool.
// Each MCP client connection gets its own Bedrock session so concurrent
// clients never share state.
func NewServer(m *muse.Muse) *server.MCPServer {
	srv := server.NewMCPServer("muse", "0.1.0", server.WithToolCapabilities(false))

	// Map from MCP client session ID → muse Bedrock session ID.
	var mu sync.Mutex
	museSessionByClient := make(map[string]string)

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

			// Resolve the MCP-level client session to a muse Bedrock session.
			clientID := ""
			if cs := server.ClientSessionFromContext(ctx); cs != nil {
				clientID = cs.SessionID()
			}

			mu.Lock()
			museSessionID := museSessionByClient[clientID]
			mu.Unlock()

			// Stream thinking tokens as log notifications to keep the
			// connection alive during long inference calls. The calling
			// agent also benefits from seeing the muse's reasoning chain.
			mcpServer := server.ServerFromContext(ctx)
			var thinking strings.Builder
			var notifyBuf strings.Builder
			var lastFlush time.Time
			notifyFailed := false

			streamFunc := func(delta inference.StreamDelta) {
				if !delta.Thinking {
					return
				}
				thinking.WriteString(delta.Text)
				notifyBuf.WriteString(delta.Text)

				if notifyFailed || time.Since(lastFlush) < flushInterval {
					return
				}
				lastFlush = time.Now()
				if err := mcpServer.SendNotificationToClient(ctx, "notifications/message", map[string]any{
					"level":  "info",
					"logger": "muse",
					"data":   notifyBuf.String(),
				}); err != nil {
					notifyFailed = true // stop sending; don't abort inference
				}
				notifyBuf.Reset()
			}

			result, err := m.Ask(ctx, muse.AskInput{
				Question:   question,
				SessionID:  museSessionID,
				New:        museSessionID == "", // First call for this client; don't resume latest from "muse ask".
				StreamFunc: streamFunc,
			})
			// Flush any remaining thinking tokens that didn't hit the
			// time threshold.
			if !notifyFailed && notifyBuf.Len() > 0 {
				_ = mcpServer.SendNotificationToClient(ctx, "notifications/message", map[string]any{
					"level":  "info",
					"logger": "muse",
					"data":   notifyBuf.String(),
				})
			}
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to ask: %v", err)), nil
			}

			mu.Lock()
			museSessionByClient[clientID] = result.SessionID
			mu.Unlock()

			// Include the muse's reasoning chain when available so the
			// calling agent can calibrate its use of the response.
			response := result.Response
			if thinking.Len() > 0 {
				response = fmt.Sprintf("<thinking>\n%s\n</thinking>\n\n%s", thinking.String(), response)
			}
			return mcp.NewToolResultText(response), nil
		},
	)
	return srv
}
