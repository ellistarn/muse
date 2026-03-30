package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/muse"
)

func newAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <source>",
		Short: "Add a conversation source",
		Long: `Activates a conversation source and syncs its conversations. The source
will be included in future "muse compose" runs automatically.

Run "muse sources" to see available sources and their status.`,
		Example: `  muse add github    # add GitHub PRs and issues
  muse add slack     # add Slack conversations`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			source := args[0]

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			// Create observation directory so the source is remembered
			if err := compose.EnsureSourceDir(ctx, store, source); err != nil {
				return err
			}

			// Sync conversations for this source
			result, err := muse.Upload(ctx, store, syncProgressRenderer(), source)
			if err != nil {
				return err
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Added %s (%d conversations)\n\n", source, result.Total)
			return printSources(ctx, cmd.OutOrStdout(), store)
		},
	}
}
