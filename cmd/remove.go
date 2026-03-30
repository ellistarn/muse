package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/compose"
)

func newRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <source>",
		Short: "Remove a conversation source",
		Long: `Deactivates a conversation source by removing its observations. The source
will be excluded from future "muse compose" runs. Cached conversations are
kept so re-adding the source doesn't require re-downloading.

Run "muse sources" to see available sources and their status.`,
		Example: `  muse remove github    # stop including GitHub in compose
  muse remove slack     # stop including Slack in compose`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			source := args[0]

			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if err := compose.RemoveSource(ctx, store, source); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", source)
			return nil
		},
	}
}
