package main

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	mcpserver "github.com/ellistarn/shade/internal/mcp"
	"github.com/ellistarn/shade/internal/shade"
)

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "listen",
		Short: "Start the shade MCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireBucket(); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := shade.New(ctx, bucket)
			if err != nil {
				return err
			}
			defer s.Close()
			srv := mcpserver.NewServer(s)
			return server.ServeStdio(srv)
		},
	}
}
