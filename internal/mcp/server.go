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

// flushInterval controls how often thinking tokens are flushed as progress
// notifications to keep the MCP connection alive during long inference calls.
const flushInterval = 3 * time.Second

// leadingMessage is the first progress message sent before any inference
// output, signaling to clients that this is a long extended-thinking call.
const leadingMessage = "Consulting the muse (extended thinking — this may take a minute)…"

// notifier delivers a single MCP notification to the connected client. It
// exists to invert the dependency on the mcp-go server so the keepalive
// decision logic (which notification to send, and with what payload) can be
// tested without a live client connection. The production implementation is
// the mcp-go server itself.
type notifier interface {
	SendNotificationToClient(ctx context.Context, method string, params map[string]any) error
}

// keepalive emits progress notifications during a long inference call so the
// client's per-request timeout keeps resetting. Clients reset that timeout on
// notifications/progress — not on log notifications — so the notification TYPE
// is load-bearing: this MUST send "notifications/progress" carrying the
// request's progress token, never a log notification.
//
// When the client did not supply a progress token there is nothing to
// associate a reset with, so keepalive emits nothing.
type keepalive struct {
	ctx       context.Context
	notifier  notifier
	token     mcp.ProgressToken
	progress  float64
	lastFlush time.Time
	failed    bool
}

func newKeepalive(ctx context.Context, n notifier, token mcp.ProgressToken) *keepalive {
	return &keepalive{ctx: ctx, notifier: n, token: token}
}

// send emits one progress notification carrying message. The progress value
// must strictly increase per the MCP spec. No fixed total is sent since
// inference length is unknown; the rising progress plus the message signals
// to clients that the call is expected to take a while. It is a no-op once a
// send has failed (stop trying, but never abort inference) or when no progress
// token was supplied.
func (k *keepalive) send(message string) {
	if k.failed || k.token == nil {
		return
	}
	k.lastFlush = time.Now()
	k.progress++
	if err := k.notifier.SendNotificationToClient(k.ctx, "notifications/progress", map[string]any{
		"progressToken": k.token,
		"progress":      k.progress,
		"message":       message,
	}); err != nil {
		k.failed = true
	}
}

// due reports whether enough time has elapsed since the last flush to send
// another keepalive.
func (k *keepalive) due() bool {
	return !k.failed && time.Since(k.lastFlush) >= flushInterval
}

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

			var token mcp.ProgressToken
			if req.Params.Meta != nil {
				token = req.Params.Meta.ProgressToken
			}
			ka := newKeepalive(ctx, server.ServerFromContext(ctx), token)

			// Leading tick: announce up front that this is a long,
			// max-thinking inference call, and cover the gap before Bedrock
			// emits its first delta (otherwise unprotected by the cadence).
			ka.send(leadingMessage)

			// Accumulate the full reasoning chain for inclusion in the
			// response; stream chunks of it through the keepalive so the
			// calling agent can observe the muse's reasoning as it goes.
			var thinking strings.Builder
			var pending strings.Builder
			streamFunc := func(delta inference.StreamDelta) {
				if !delta.Thinking {
					return
				}
				thinking.WriteString(delta.Text)
				pending.WriteString(delta.Text)
				if !ka.due() {
					return
				}
				ka.send(pending.String())
				pending.Reset()
			}

			result, err := m.Ask(ctx, muse.AskInput{
				Question:   question,
				SessionID:  museSessionID,
				New:        museSessionID == "", // First call for this client; don't resume latest from "muse ask".
				StreamFunc: streamFunc,
			})
			// Flush any remaining thinking tokens that didn't hit the cadence.
			if pending.Len() > 0 {
				ka.send(pending.String())
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
