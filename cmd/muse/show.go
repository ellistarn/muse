package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/log"
	"github.com/ellistarn/muse/internal/storage"
)

func newShowCmd() *cobra.Command {
	var diff bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print muse.md",
		Long: `Prints your current muse.md to stdout. If no muse exists yet, prompts
you to run 'muse distill'.

Use --diff to print the changelog from the latest distill.`,
		Example: `  muse show          # print the muse
  muse show --diff   # print what changed in the latest distill`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			store, err := newStore(ctx)
			if err != nil {
				return err
			}

			if diff {
				return runShowDiff(cmd, store)
			}

			log.Println("Loading muse...")
			soul, err := store.GetMuse(ctx)
			if err != nil {
				if !storage.IsNotFound(err) {
					return fmt.Errorf("failed to load muse: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "No muse found. Run 'muse distill' to generate one from memories.")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(soul))
			fmt.Fprintf(cmd.ErrOrStderr(), "muse.md: ~%d tokens\n", inference.EstimateTokens(soul))
			return nil
		},
	}
	cmd.Flags().BoolVar(&diff, "diff", false, "print what changed in the latest distill")
	return cmd
}

func runShowDiff(cmd *cobra.Command, store storage.Store) error {
	ctx := cmd.Context()
	muses, err := store.ListMuses(ctx)
	if err != nil {
		return fmt.Errorf("failed to list muse history: %w", err)
	}
	if len(muses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No muse found. Run 'muse distill' to generate one from memories.")
		return nil
	}
	latest := muses[len(muses)-1]
	d, err := store.GetMuseDiff(ctx, latest)
	if err != nil {
		if storage.IsNotFound(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "No diff available for the latest version. Re-run 'muse distill' to generate one.")
			return nil
		}
		return fmt.Errorf("failed to load diff: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(d))
	return nil
}
