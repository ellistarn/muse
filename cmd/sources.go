package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/conversation"
)

func newSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List available conversation sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, s := range conversation.Sources() {
				tag := ""
				if s.OptIn {
					tag = " (opt-in)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-14s%s\n", s.Name, tag)
			}
			return nil
		},
	}
}
