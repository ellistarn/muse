package main

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	mcpserver "github.com/ellistarn/muse/internal/mcp"
	"github.com/ellistarn/muse/internal/muse"
)

func newListenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "listen",
		Short: "Start the muse MCP server",
		Long: `Starts an MCP server over stdio that exposes an "ask" tool so agents can
query your muse programmatically. Runs until stdin is closed or interrupted.

Add this to your agent's MCP config:

  {
    "mcpServers": {
      "<your-name>": {
        "command": "muse",
        "args": ["listen"]
      }
    }
  }`,
		Example: `  muse listen`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			m, err := muse.New(ctx, bucket)
			if err != nil {
				return err
			}
			srv := mcpserver.NewServer(m)
			return server.ServeStdio(srv)
		},
	}
}
