package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
)

func newSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List available conversation sources",
		Long: `Lists all conversation sources and their status.

Active sources have an observation directory and are included when running
"muse compose" with no arguments. To activate a source, run "muse compose <source>".
To deactivate, delete its observation directory (e.g. rm -rf ~/.muse/observations/github/).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			active, err := compose.ListObservationSources(ctx, store)
			if err != nil {
				return err
			}
			activeSet := make(map[string]bool, len(active))
			for _, s := range active {
				activeSet[s] = true
			}

			for _, s := range conversation.Sources() {
				status := "inactive"
				if activeSet[s.Name] {
					status = "active"
				}
				tag := ""
				if s.OptIn {
					tag = " (opt-in)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %-14s %-10s%s\n", s.Name, status, tag)
			}
			return nil
		},
	}
}
